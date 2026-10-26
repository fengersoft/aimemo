package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestOpenCreatesSchemaWithTimestamps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aimemo.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	tables := map[string]bool{}
	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		t.Fatalf("query schema: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		tables[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	for _, want := range []string{"categories", "memories", "memory_tags", "memories_fts"} {
		if !tables[want] {
			t.Errorf("missing table %q", want)
		}
	}

	for _, tbl := range []string{"categories", "memories", "memory_tags"} {
		cols := map[string]bool{}
		crows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, tbl)
		if err != nil {
			t.Fatalf("table_info %s: %v", tbl, err)
		}
		for crows.Next() {
			var name string
			if err := crows.Scan(&name); err != nil {
				t.Fatalf("scan cols: %v", err)
			}
			cols[name] = true
		}
		if err := crows.Err(); err != nil {
			t.Fatalf("cols rows %s: %v", tbl, err)
		}
		crows.Close()
		if !cols["created_at"] {
			t.Errorf("table %s missing created_at", tbl)
		}
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aimemo.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	s2.Close()
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("expected an error for an empty path")
	}
}

func TestOpenHandlesSpecialCharactersInPath(t *testing.T) {
	// A "file:" URI prefix would truncate the path at "#" and decode "%20".
	path := filepath.Join(t.TempDir(), "ha#sh a%20b.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if _, err := s.db.Exec(`CREATE TABLE probe(x)`); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("database was not created at %q: %v", path, err)
	}
}

func TestNowUsesCanonicalFormat(t *testing.T) {
	got := Now()
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}Z$`).MatchString(got) {
		t.Errorf("Now() = %q, want the canonical UTC format", got)
	}
}

func TestNowIsStrictlyIncreasing(t *testing.T) {
	// The wall clock of the host may advance only every few milliseconds, so
	// two writes in a row must still get different timestamps.
	prev := Now()
	for i := 0; i < 100; i++ {
		got := Now()
		if got <= prev {
			t.Fatalf("Now() = %q, want a value greater than %q", got, prev)
		}
		prev = got
	}
}

func TestWithTxCommitsAndRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aimemo.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if err := s.withTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO categories(name, created_at, updated_at) VALUES('a', ?, ?)`, Now(), Now())
		return err
	}); err != nil {
		t.Fatalf("withTx commit: %v", err)
	}

	boom := errors.New("boom")
	if err := s.withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO categories(name, created_at, updated_at) VALUES('b', ?, ?)`, Now(), Now()); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("withTx error = %v, want %v", err, boom)
	}

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM categories WHERE name = 'b'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("rolled back row is still present")
	}
}

func TestOpenMigratesV1Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`
CREATE TABLE categories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE COLLATE NOCASE,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    category_id INTEGER NOT NULL REFERENCES categories(id),
    type TEXT NOT NULL DEFAULT 'note',
    importance INTEGER NOT NULL DEFAULT 3,
    title TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL,
    scope TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
INSERT INTO categories(name, created_at, updated_at) VALUES('legacy', '2024-01-01T00:00:00.000000Z', '2024-01-01T00:00:00.000000Z');
INSERT INTO memories(category_id, content, created_at, updated_at)
VALUES(1, 'legacy body', '2024-01-01T00:00:00.000000Z', '2024-01-01T00:00:00.000000Z');
`)
	if err != nil {
		t.Fatal(err)
	}
	raw.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer s.Close()

	m, err := s.Get(1, "")
	if err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if m.Content != "legacy body" || m.Workspace != "" || m.Confidence != 3 {
		t.Errorf("migrated row = %+v", m)
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Errorf("user_version = %d, want %d", version, schemaVersion)
	}

	// Opening twice must stay idempotent.
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
}
