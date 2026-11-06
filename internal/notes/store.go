package notes

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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
	if _, err := s.db.Exec(`DELETE FROM notes; DELETE FROM links; DELETE FROM notes_fts;`); err != nil {
		return Stats{}, err
	}
	return s.Sync(dir)
}

// Sync reindexes files whose content hash changed and removes deleted files.
func (s *Store) Sync(dir string) (Stats, error) {
	files, err := markdownFiles(dir)
	if err != nil {
		return Stats{}, err
	}
	seen := map[string]struct{}{}
	var stats Stats
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return stats, err
		}
		note := Parse(path, data)
		seen[path] = struct{}{}
		var existing string
		err = s.db.QueryRow(`SELECT content_hash FROM notes WHERE path = ?`, path).Scan(&existing)
		if err == nil && existing == note.Hash {
			continue
		}
		if err := s.upsert(note); err != nil {
			return stats, err
		}
		stats.Updated++
	}
	rows, err := s.db.Query(`SELECT path FROM notes`)
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
	_ = rows.Close()
	for _, path := range gone {
		if err := s.deletePath(path); err != nil {
			return stats, err
		}
		stats.Removed++
	}
	if err := s.resolveLinks(); err != nil {
		return stats, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM notes`).Scan(&stats.Notes); err != nil {
		return stats, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM links`).Scan(&stats.Links); err != nil {
		return stats, err
	}
	stats.Warnings, err = s.Violations()
	return stats, err
}

func (s *Store) upsert(note Note) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var oldID string
	_ = tx.QueryRow(`SELECT id FROM notes WHERE path = ?`, note.Path).Scan(&oldID)
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
	if _, err := tx.Exec(`
INSERT INTO notes (id, path, title, type, status, step_order, tags, body, content_hash, embedding)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
ON CONFLICT(id) DO UPDATE SET
  path = excluded.path, title = excluded.title, type = excluded.type, status = excluded.status,
  step_order = excluded.step_order, tags = excluded.tags, body = excluded.body,
  content_hash = excluded.content_hash, embedding = NULL`,
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
	return tx.Commit()
}

func (s *Store) deletePath(path string) error {
	var id string
	if err := s.db.QueryRow(`SELECT id FROM notes WHERE path = ?`, path).Scan(&id); err != nil {
		return nil
	}
	if _, err := s.db.Exec(`DELETE FROM notes_fts WHERE id = ?`, id); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM links WHERE src = ?`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM notes WHERE id = ?`, id)
	return err
}

func (s *Store) resolveLinks() error {
	_, err := s.db.Exec(`
UPDATE links SET dst = (
  SELECT notes.id FROM notes
  WHERE notes.id = links.target OR lower(notes.title) = lower(links.target)
  LIMIT 1
)`)
	return err
}

// Violations reports goals marked done while a linked step is still open.
func (s *Store) Violations() ([]string, error) {
	rows, err := s.db.Query(`
SELECT g.id, s.id, s.status
FROM notes g
JOIN links l ON l.src = g.id OR l.dst = g.id
JOIN notes s ON s.id = CASE WHEN l.src = g.id THEN l.dst ELSE l.src END
WHERE g.type = 'goal' AND g.status IN ('done', 'closed')
  AND s.type = 'steps' AND s.status NOT IN ('done', 'closed', 'dropped')
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
	return notes, rows.Err()
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
			if name != "." && strings.HasPrefix(name, ".") {
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
