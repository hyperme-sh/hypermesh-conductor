package proxy

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

var errTaskBudget = errors.New("task output allowance exhausted, recipe changed, or step already reserved")

// One statement under SQLite's writer lock admits a fresh attempt. Unknown
// outcomes retain the entire charge even when the caller or process disappears.
func reserveOutput(path, task, step, recipe string, limit, charge int) (*sql.DB, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: absolute}
	db, err := sql.Open("sqlite", uri.String()+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS output_attempts (
 task TEXT NOT NULL, step TEXT NOT NULL, recipe TEXT NOT NULL,
 charge INTEGER NOT NULL CHECK(charge>=0), PRIMARY KEY(task,step)
 ) WITHOUT ROWID`)
	if err == nil {
		var result sql.Result
		result, err = db.Exec(`INSERT INTO output_attempts(task,step,recipe,charge)
 SELECT ?,?,?,? WHERE (SELECT COALESCE(SUM(charge),0) FROM output_attempts WHERE task=?) <= ?
 AND NOT EXISTS(SELECT 1 FROM output_attempts WHERE task=? AND recipe<>?)
 ON CONFLICT(task,step) DO NOTHING`, task, step, recipe, charge, task, limit-charge, task, recipe)
		if err == nil {
			var n int64
			n, err = result.RowsAffected()
			if err == nil && n != 1 {
				err = errTaskBudget
			}
		}
	}
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("reserve task output: %w", err)
	}
	return db, nil
}

type budgetContextKey struct{}
