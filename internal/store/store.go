package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store is a handle to the aimemo SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens the database at path, creating the directory and schema if needed.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("open database: path must not be empty")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	// The path is used verbatim instead of behind a "file:" URI prefix: the
	// URI form treats "#" and "%" as escapes and would silently open a
	// different database. modernc still applies every _pragma to each pooled
	// connection.
	//
	// _txlock=immediate starts write transactions as BEGIN IMMEDIATE. The
	// default deferred mode reads first and then upgrades to a write lock,
	// which fails with SQLITE_BUSY without honouring busy_timeout when two
	// agents write at the same time.
	dsn := fmt.Sprintf(
		"%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_txlock=immediate",
		filepath.ToSlash(path),
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Now returns the canonical timestamp used by every table: UTC RFC3339 with
// microsecond precision, so lexical order equals chronological order and two
// writes in the same millisecond still differ.
func Now() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
}

// querier is satisfied by both *sql.DB and *sql.Tx.
type querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// withTx runs fn inside a transaction, rolling back on error.
func (s *Store) withTx(fn func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	// Rollback after a successful commit is a no-op, so this is safe and it
	// also releases the transaction when fn panics.
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
