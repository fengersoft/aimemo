package store

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
