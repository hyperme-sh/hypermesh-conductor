package capabilitydispatch

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type DurableJournal struct{ db *sql.DB }

func OpenJournal(path string) (*DurableJournal, error) {
	if privateDirectory(filepath.Dir(path)) != nil {
		return nil, Refused
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		file.Close()
		directory, err := os.Open(filepath.Dir(path))
		if err != nil {
			return nil, Refused
		}
		err = directory.Sync()
		directory.Close()
		if err != nil {
			return nil, Refused
		}
	} else if !os.IsExist(err) {
		return nil, Refused
	}
	checked, err := ownedFile(path)
	if err != nil {
		return nil, Refused
	}
	stat, err := checked.Stat()
	checked.Close()
	if err != nil || stat.Mode().Perm() != 0600 {
		return nil, Refused
	}
	location := (&url.URL{Scheme: "file", Path: path}).String() + "?_pragma=busy_timeout(3000)&_pragma=synchronous(FULL)"
	db, err := sql.Open("sqlite", location)
	if err != nil {
		return nil, Refused
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS capability_dispatch_calls (id TEXT PRIMARY KEY, hash TEXT NOT NULL)`); err != nil {
		db.Close()
		return nil, Refused
	}
	return &DurableJournal{db}, nil
}
func (j *DurableJournal) Close() error { return j.db.Close() }
func (j *DurableJournal) Reserve(id, hash string) error {
	result, err := j.db.Exec(`INSERT INTO capability_dispatch_calls(id,hash) SELECT ?,? WHERE (SELECT COUNT(*) FROM capability_dispatch_calls)<100000 ON CONFLICT(id) DO NOTHING`, id, hash)
	if err != nil {
		return Refused
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return Refused
	}
	return nil
}
