package store

import (
	"database/sql"
	"fmt"
)

// schemaVersion is bumped whenever the memories table gains columns.
const schemaVersion = 3

// schemaSQL is applied on every Open. Every statement is idempotent.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS categories (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS memories (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    category_id INTEGER NOT NULL REFERENCES categories(id),
    type        TEXT    NOT NULL DEFAULT 'note'
                        CHECK (type IN ('summary','important','note')),
    importance  INTEGER NOT NULL DEFAULT 3
                        CHECK (importance BETWEEN 1 AND 5),
    title       TEXT    NOT NULL DEFAULT '',
    content     TEXT    NOT NULL,
    scope       TEXT    NOT NULL DEFAULT '',
    source      TEXT    NOT NULL DEFAULT '',
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS memory_tags (
    memory_id  INTEGER NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
    tag        TEXT    NOT NULL,
    created_at TEXT    NOT NULL,
    PRIMARY KEY (memory_id, tag)
);

CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
    title, content, tags,
    tokenize='unicode61 remove_diacritics 2'
);

CREATE INDEX IF NOT EXISTS idx_memories_category   ON memories(category_id);
CREATE INDEX IF NOT EXISTS idx_memories_created    ON memories(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_memories_scope      ON memories(scope);
CREATE INDEX IF NOT EXISTS idx_memories_type       ON memories(type);
CREATE INDEX IF NOT EXISTS idx_memories_importance ON memories(importance DESC);
CREATE INDEX IF NOT EXISTS idx_memory_tags_tag     ON memory_tags(tag);
`

// memoryColumnsV2 are the columns added after the first release. They are
// applied with ALTER TABLE so existing databases migrate in place.
var memoryColumnsV2 = []struct{ name, ddl string }{
	{"workspace", "workspace TEXT NOT NULL DEFAULT ''"},
	{"problem", "problem TEXT NOT NULL DEFAULT ''"},
	{"solution", "solution TEXT NOT NULL DEFAULT ''"},
	{"pitfall", "pitfall TEXT NOT NULL DEFAULT ''"},
	{"example", "example TEXT NOT NULL DEFAULT ''"},
	{"confidence", "confidence INTEGER NOT NULL DEFAULT 3"},
	{"valid_until", "valid_until TEXT NOT NULL DEFAULT ''"},
	{"supersedes", "supersedes INTEGER NOT NULL DEFAULT 0"},
	{"last_used_at", "last_used_at TEXT NOT NULL DEFAULT ''"},
	{"use_count", "use_count INTEGER NOT NULL DEFAULT 0"},
	{"helpful_count", "helpful_count INTEGER NOT NULL DEFAULT 0"},
	{"wrong_count", "wrong_count INTEGER NOT NULL DEFAULT 0"},
	{"features", "features BLOB"},
}

// migrate adds any missing v2 columns and records the schema version.
func migrate(db *sql.DB) error {
	cols, err := tableColumns(db, "memories")
	if err != nil {
		return err
	}
	for _, c := range memoryColumnsV2 {
		if cols[c.name] {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE memories ADD COLUMN ` + c.ddl); err != nil {
			return fmt.Errorf("add column %s: %w", c.name, err)
		}
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_memories_workspace ON memories(workspace)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_valid_until ON memories(valid_until)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version >= schemaVersion {
		return nil
	}
	_, err = db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion))
	return err
}

// tableColumns returns the column names of a table.
func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var (
			cid       int
			name      string
			typ       string
			notNull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dfltValue, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}
