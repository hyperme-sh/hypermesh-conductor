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

var errTaskBudget = errors.New("task token allowance exhausted, recipe changed, or step already reserved")

// One statement under SQLite's writer lock admits a fresh attempt. Unknown
// outcomes retain the entire charge even when the caller or process disappears.
type tokenBudget struct {
	outputLimit, totalLimit int
	input, output           int
}

func reserveTokens(path, task, step, recipe string, budget tokenBudget) (*sql.DB, error) {
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
	db, err := sql.Open("sqlite", uri.String()+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	err = admitAttempt(db, task, step, recipe, budget)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("reserve task output: %w", err)
	}
	return db, nil
}

// Take the writer lock before checking the schema, so two processes opening a
// pre-input-accounting ledger can upgrade it and admit attempts safely.
func admitAttempt(db *sql.DB, task, step, recipe string, b tokenBudget) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS output_attempts (
 task TEXT NOT NULL, step TEXT NOT NULL, recipe TEXT NOT NULL,
 charge INTEGER NOT NULL CHECK(charge>=0), input_charge INTEGER NOT NULL DEFAULT 0 CHECK(input_charge>=0),
 PRIMARY KEY(task,step)) WITHOUT ROWID`)
	if err != nil {
		return err
	}
	var present int
	if err := tx.QueryRow("SELECT count(*) FROM pragma_table_info('output_attempts') WHERE name='input_charge'").Scan(&present); err != nil {
		return err
	}
	if present == 0 {
		if _, err := tx.Exec("ALTER TABLE output_attempts ADD COLUMN input_charge INTEGER NOT NULL DEFAULT 0 CHECK(input_charge>=0)"); err != nil {
			return err
		}
	}
	result, err := tx.Exec(`INSERT INTO output_attempts(task,step,recipe,charge,input_charge)
 SELECT ?,?,?,?,? WHERE (?=0 OR (SELECT COALESCE(SUM(charge),0) FROM output_attempts WHERE task=?) <= ?)
 AND (?=0 OR (SELECT COALESCE(SUM(charge+input_charge),0) FROM output_attempts WHERE task=?) <= ?)
 AND NOT EXISTS(SELECT 1 FROM output_attempts WHERE task=? AND recipe<>?)
 ON CONFLICT(task,step) DO NOTHING`, task, step, recipe, b.output, b.input, b.outputLimit, task, int64(b.outputLimit)-int64(b.output), b.totalLimit, task, int64(b.totalLimit)-int64(b.output)-int64(b.input), task, recipe)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errTaskBudget
	}
	return tx.Commit()
}

type budgetContextKey struct{}
