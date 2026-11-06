package notes

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

// Store is the derived SQLite index. Deleting the database and rebuilding
// reproduces it from Markdown.
type Store struct {
	db *sql.DB
}

// Open opens or creates the index database.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS notes (
  id TEXT PRIMARY KEY,
  path TEXT NOT NULL UNIQUE,
  title TEXT NOT NULL,
  type TEXT NOT NULL,
  status TEXT NOT NULL,
  step_order INTEGER NOT NULL DEFAULT 0,
  tags TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  embedding TEXT
);
CREATE TABLE IF NOT EXISTS links (
  src TEXT NOT NULL,
  target TEXT NOT NULL,
  dst TEXT,
  PRIMARY KEY (src, target)
);
CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts USING fts5(id, title, body, tags, tokenize = 'porter');
`)
	return err
}

// Stats is the result of a rebuild or sync.
type Stats struct {
	Notes    int
	Updated  int
	Removed  int
	Links    int
	Warnings []string
}

// Status is a point-in-time report.
type Status struct {
	Notes      int
	Links      int
	Embedded   int
	Generation string
	Warnings   []string
}

// Rebuild drops derived rows and indexes the directory again.
func (s *Store) Rebuild(dir string) (Stats, error) {
	return s.syncDir(dir, true)
}

// Sync reindexes files whose content hash changed and removes deleted files.
func (s *Store) Sync(dir string) (Stats, error) {
	return s.syncDir(dir, false)
}

// syncDir publishes note rows and the resolved link graph in one transaction.
// A reader therefore never observes a new content generation whose wikilinks
// are still unresolved.
func (s *Store) syncDir(dir string, replace bool) (Stats, error) {
	files, err := markdownFiles(dir)
	if err != nil {
		return Stats{}, err
	}
	incoming, warnings, err := readNotes(dir, files)
	if err != nil {
		return Stats{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Stats{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if replace {
		if _, err := tx.Exec(`DELETE FROM notes; DELETE FROM links; DELETE FROM notes_fts;`); err != nil {
			return Stats{}, err
		}
	}
	var stats Stats
	stats.Warnings = warnings
	seen := map[string]struct{}{}
	for _, note := range incoming {
		seen[note.Path] = struct{}{}
		var existing string
		err = tx.QueryRow(`SELECT content_hash FROM notes WHERE path = ?`, note.Path).Scan(&existing)
		if err == nil && existing == note.Hash {
			continue
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return stats, err
		}
		if err := upsert(tx, note); err != nil {
			var collision idCollision
			if errors.As(err, &collision) {
				stats.Warnings = append(stats.Warnings, collision.Error())
				continue
			}
			return stats, err
		}
		stats.Updated++
	}
	rows, err := tx.Query(`SELECT path FROM notes`)
	if err != nil {
		return stats, err
	}
	var gone []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			_ = rows.Close()
			return stats, err
		}
		if _, ok := seen[path]; !ok {
			gone = append(gone, path)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return stats, err
	}
	_ = rows.Close()
	for _, path := range gone {
		if err := deletePath(tx, path); err != nil {
			return stats, err
		}
		stats.Removed++
	}
	if err := resolveLinks(tx); err != nil {
		return stats, err
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&stats.Notes); err != nil {
		return stats, err
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM links`).Scan(&stats.Links); err != nil {
		return stats, err
	}
	violations, err := violations(tx)
	if err != nil {
		return stats, err
	}
	stats.Warnings = append(stats.Warnings, violations...)
	if err := tx.Commit(); err != nil {
		return stats, err
	}
	return stats, nil
}

type idCollision struct {
	id, owner, path string
}

func (e idCollision) Error() string {
	return fmt.Sprintf("note id %s belongs to %s and %s", e.id, e.owner, e.path)
}

func readNotes(dir string, files []string) ([]Note, []string, error) {
	incoming := make([]Note, 0, len(files))
	counts := map[string]int{}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		note := Parse(path, data)
		incoming = append(incoming, note)
		if note.idFromName {
			counts[note.ID]++
		}
	}
	var warnings []string
	for i, note := range incoming {
		if !note.idFromName || counts[note.ID] < 2 {
			continue
		}
		next := relativeNoteID(dir, note.Path)
		if next == note.ID {
			continue
		}
		warnings = append(warnings, fmt.Sprintf("note id %s is shared by %s; indexed as %s", note.ID, note.Path, next))
		incoming[i].ID = next
	}
	return incoming, warnings, nil
}

func relativeNoteID(dir, path string) string {
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		rel = filepath.Base(path)
	}
	rel = filepath.ToSlash(rel)
	return strings.TrimSuffix(rel, filepath.Ext(rel))
}

type sqlDB interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func upsert(tx sqlDB, note Note) error {
	var owner string
	err := tx.QueryRow(`SELECT path FROM notes WHERE id = ?`, note.ID).Scan(&owner)
	if err == nil && owner != note.Path {
		return idCollision{id: note.ID, owner: owner, path: note.Path}
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var oldID string
	err = tx.QueryRow(`SELECT id FROM notes WHERE path = ?`, note.Path).Scan(&oldID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if oldID != "" && oldID != note.ID {
		if _, err := tx.Exec(`DELETE FROM links WHERE src = ?`, oldID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM notes_fts WHERE id = ?`, oldID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM notes WHERE id = ?`, oldID); err != nil {
			return err
		}
	}
	tags := strings.Join(note.Tags, " ")
	if owner == note.Path {
		if _, err := tx.Exec(`
UPDATE notes SET title = ?, type = ?, status = ?, step_order = ?, tags = ?, body = ?, content_hash = ?, embedding = NULL
WHERE id = ? AND path = ?`,
			note.Title, note.Type, note.Status, note.Order, tags, note.Body, note.Hash, note.ID, note.Path); err != nil {
			return err
		}
	} else if _, err := tx.Exec(`
INSERT INTO notes (id, path, title, type, status, step_order, tags, body, content_hash, embedding)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		note.ID, note.Path, note.Title, note.Type, note.Status, note.Order, tags, note.Body, note.Hash); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM notes_fts WHERE id = ?`, note.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO notes_fts (id, title, body, tags) VALUES (?, ?, ?, ?)`, note.ID, note.Title, note.Body, tags); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM links WHERE src = ?`, note.ID); err != nil {
		return err
	}
	for _, target := range note.Links {
		if _, err := tx.Exec(`INSERT INTO links (src, target, dst) VALUES (?, ?, NULL)`, note.ID, target); err != nil {
			return err
		}
	}
	return nil
}

func deletePath(tx sqlDB, path string) error {
	var id string
	if err := tx.QueryRow(`SELECT id FROM notes WHERE path = ?`, path).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if _, err := tx.Exec(`DELETE FROM notes_fts WHERE id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM links WHERE src = ?`, id); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM notes WHERE id = ?`, id)
	return err
}

func resolveLinks(tx sqlDB) error {
	_, err := tx.Exec(`
UPDATE links SET dst = (
  SELECT notes.id FROM notes
  WHERE notes.id = links.target OR lower(notes.title) = lower(links.target)
  LIMIT 1
)`)
	return err
}

// Violations reports goals marked done while a linked step is still open.
func (s *Store) Violations() ([]string, error) {
	return violations(s.db)
}

func violations(db sqlDB) ([]string, error) {
	rows, err := db.Query(`
SELECT g.id, s.id, s.status
FROM notes g
JOIN links l ON l.src = g.id OR l.dst = g.id
JOIN notes s ON s.id = CASE WHEN l.src = g.id THEN l.dst ELSE l.src END
WHERE g.type = 'goal' AND g.status IN ('done', 'closed')
  AND s.type = 'steps' AND s.status NOT IN ('done', 'closed', 'dropped', 'drop')
  AND s.id != g.id
ORDER BY g.id, s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	seen := map[string]struct{}{}
	for rows.Next() {
		var goal, step, status string
		if err := rows.Scan(&goal, &step, &status); err != nil {
			return nil, err
		}
		line := fmt.Sprintf("goal %s cannot close while step %s is %s", goal, step, status)
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		out = append(out, line)
	}
	return out, rows.Err()
}

// Status reports index counts. It does not include note bodies.
func (s *Store) Status() (Status, error) {
	var status Status
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&status.Notes); err != nil {
		return status, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM links`).Scan(&status.Links); err != nil {
		return status, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM notes WHERE embedding IS NOT NULL AND embedding != ''`).Scan(&status.Embedded); err != nil {
		return status, err
	}
	var err error
	status.Generation, err = s.Generation()
	if err != nil {
		return status, err
	}
	status.Warnings, err = s.Violations()
	return status, err
}

// Generation is a hash of every note content hash. An edit changes it.
func (s *Store) Generation() (string, error) {
	rows, err := s.db.Query(`SELECT id, content_hash FROM notes ORDER BY id`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	h := sha256.New()
	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			return "", err
		}
		_, _ = h.Write([]byte(id))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(hash))
		_, _ = h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil)), rows.Err()
}

// Search returns note ids for a full-text query.
func (s *Store) Search(text string, limit int) ([]string, error) {
	query := ftsQuery(text)
	if query == "" || limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT id FROM notes_fts WHERE notes_fts MATCH ? LIMIT ?`, query, limit)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Get loads one note. The bool is false when the id is absent.
func (s *Store) Get(id string) (Note, bool, error) {
	var note Note
	var tags string
	err := s.db.QueryRow(`SELECT id, path, title, type, status, step_order, tags, body, content_hash FROM notes WHERE id = ?`, id).
		Scan(&note.ID, &note.Path, &note.Title, &note.Type, &note.Status, &note.Order, &tags, &note.Body, &note.Hash)
	if err == sql.ErrNoRows {
		return Note{}, false, nil
	}
	if err != nil {
		return Note{}, false, err
	}
	if tags != "" {
		note.Tags = strings.Fields(tags)
	}
	return note, true, nil
}

// Neighbors returns note ids one hop away by link or backlink.
func (s *Store) Neighbors(id string) ([]string, error) {
	rows, err := s.db.Query(`
SELECT dst FROM links WHERE src = ? AND dst IS NOT NULL
UNION
SELECT src FROM links WHERE dst = ?`, id, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var next string
		if err := rows.Scan(&next); err != nil {
			return nil, err
		}
		if next != "" && next != id {
			ids = append(ids, next)
		}
	}
	sort.Strings(ids)
	return ids, rows.Err()
}

// All returns every note ordered by type, step order, and id.
func (s *Store) All() ([]Note, error) {
	rows, err := s.db.Query(`SELECT id, path, title, type, status, step_order, tags, body, content_hash FROM notes ORDER BY type, step_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var notes []Note
	for rows.Next() {
		var note Note
		var tags string
		if err := rows.Scan(&note.ID, &note.Path, &note.Title, &note.Type, &note.Status, &note.Order, &tags, &note.Body, &note.Hash); err != nil {
			return nil, err
		}
		if tags != "" {
			note.Tags = strings.Fields(tags)
		}
		notes = append(notes, note)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	links, err := linksBySource(s.db)
	if err != nil {
		return nil, err
	}
	for i := range notes {
		notes[i].Links = links[notes[i].ID]
	}
	return notes, nil
}

func linksBySource(db sqlDB) (map[string][]string, error) {
	rows, err := db.Query(`SELECT src, target FROM links ORDER BY src, target`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var src, target string
		if err := rows.Scan(&src, &target); err != nil {
			return nil, err
		}
		out[src] = append(out[src], target)
	}
	return out, rows.Err()
}

// MissingEmbeddings returns notes that do not yet have a vector.
func (s *Store) MissingEmbeddings() ([]Note, error) {
	rows, err := s.db.Query(`SELECT id, path, title, type, status, step_order, tags, body, content_hash FROM notes WHERE embedding IS NULL OR embedding = '' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var notes []Note
	for rows.Next() {
		var note Note
		var tags string
		if err := rows.Scan(&note.ID, &note.Path, &note.Title, &note.Type, &note.Status, &note.Order, &tags, &note.Body, &note.Hash); err != nil {
			return nil, err
		}
		notes = append(notes, note)
	}
	return notes, rows.Err()
}

// SaveEmbedding stores a cheap-tier vector. An empty vector clears it.
func (s *Store) SaveEmbedding(id string, vector []float64) error {
	raw, err := json.Marshal(vector)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE notes SET embedding = ? WHERE id = ?`, string(raw), id)
	return err
}

// HasEmbedding reports whether a note already has an embedding for its hash.
func (s *Store) HasEmbedding(id string) (bool, error) {
	var embedding sql.NullString
	err := s.db.QueryRow(`SELECT embedding FROM notes WHERE id = ?`, id).Scan(&embedding)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return embedding.Valid && embedding.String != "", nil
}

func markdownFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			if filepath.Clean(path) != filepath.Clean(dir) && name != "." && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(name), ".md") {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func ftsQuery(text string) string {
	var tokens []string
	seen := map[string]struct{}{}
	for _, field := range strings.Fields(text) {
		var b strings.Builder
		for _, r := range field {
			if unicodeLetter(r) {
				b.WriteRune(r)
			}
		}
		token := strings.ToLower(b.String())
		if len(token) < 3 {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		tokens = append(tokens, `"`+token+`"`)
		if len(tokens) == 8 {
			break
		}
	}
	return strings.Join(tokens, " OR ")
}

func unicodeLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}
