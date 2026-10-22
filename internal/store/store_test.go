package store

import (
	"path/filepath"
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
