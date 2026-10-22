# aimemo Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 构建一个供 AI 通过命令行调用的记忆存储程序，AI 可自行决定分类、记录摘要或重要信息，用 SQLite 持久化，所有数据表带创建时间。

**Architecture:** Go 单二进制 CLI，分两层——`internal/store` 封装全部 SQL 与中文全文检索逻辑，`internal/cli` 负责 flag 解析、JSON 输出信封与退出码，`main.go` 仅做分发。检索基于 SQLite FTS5 + `unicode61` 分词器，并对所有中文字符做**双侧空格填充**变换，使中文可按子串精确匹配。

**Tech Stack:** Go 1.26 / `modernc.org/sqlite`（纯 Go，含 FTS5，无需 CGO）/ 标准库 `flag` `encoding/json` `database/sql`

**设计文档:** `docs/superpowers/specs/2026-10-22-aimemo-design.md`

---

## 前置：已实测验证的环境事实（勿重复验证）

- `modernc.org/sqlite` v1.34.5 内含 SQLite 3.46.0，FTS5 / trigram / unicode61 三种分词器均可用
- DSN 形如 `file:<path>?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)`，`_pragma` 对连接池中每条连接生效
- `unicode61` 不做变换时，整句中文是**单个 token**，搜索"数据"无法命中"使用数据库" → 必须做 CJK 变换
- CJK 变换必须**双侧**补空格：仅后补空格会让 `Redis作为` 合并为单 token，搜索 `redis` 命中 0 条
- FTS5 phrase（`"数 据 库"`）实现位置连续的精确子串匹配
- bm25 对中文几乎无区分度 → 默认排序为 `importance DESC, created_at DESC`
- 查询串**不可**原样传给 MATCH（`x" AND y` 会抛 `unterminated string`）→ 必须程序化构建表达式
- 本仓库当前**不是** git 仓库，Task 1 执行 `git init`，此后每个任务末尾提交一次

**计划代码已预验证：** 本计划中的 Go 代码块已被抽取成完整工程并实际编译运行（`go vet ./...` 无输出、`go test ./... -count=1` 全部通过、端到端冒烟命令输出符合契约）。实现时若发现与本计划不一致，以能通过这些检查为准。

---

## File Structure

```
main.go                      入口，仅调用 cli.Run 并 os.Exit
internal/store/
  schema.go                  DDL 常量
  store.go                   Store / Open / Close / 事务 / 时间戳
  fts.go                     CJK 变换、FTS 查询构建、索引同步、命中上下文
  category.go                类别创建与列举
  memory.go                  记忆 CRUD、校验
  query.go                   过滤条件、排序、列表与搜索
internal/cli/
  root.go                    子命令分发、输出信封、错误码、退出码、全局 flag
  render.go                  JSON / text 渲染
  commands.go                9 个子命令实现
tests/
  e2e_test.go                端到端测试
```

分层约束：`internal/store` 不 import `internal/cli`；`internal/cli` 不写 SQL。

---

### Task 1: 项目骨架与依赖

**Files:**
- Create: `.gitignore`
- Modify: `go.mod`

- [ ] **Step 1: 初始化 git 仓库并添加 .gitignore**

```bash
cd D:/work2026/aimemo
git init
```

`.gitignore` 内容：

```
*.exe
aimemo.db
aimemo.db-shm
aimemo.db-wal
```

- [ ] **Step 2: 添加 SQLite 依赖**

```bash
cd D:/work2026/aimemo
go get modernc.org/sqlite@v1.34.5
```

Expected: `go.mod` 中出现 `require modernc.org/sqlite v1.34.5`

- [ ] **Step 3: 确认当前代码可编译**

```bash
cd D:/work2026/aimemo
go build ./...
```

Expected: 无输出（成功）

- [ ] **Step 4: 提交**

```bash
cd D:/work2026/aimemo
git add .gitignore go.mod go.sum
git commit -m "chore: initialize module and add sqlite dependency"
```

---

### Task 2: store.Open 与 schema

**Files:**
- Create: `internal/store/schema.go`
- Create: `internal/store/store.go`
- Create: `internal/store/store_test.go`

- [ ] **Step 1: 写失败的测试**

`internal/store/store_test.go`：

```go
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
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd D:/work2026/aimemo
go test ./internal/store/ 2>&1
```

Expected: FAIL — `undefined: Open`

- [ ] **Step 3: 创建 internal/store/schema.go**

```go
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
```

- [ ] **Step 4: 创建 internal/store/store.go**

```go
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// nowMu guards lastNow.
var (
	nowMu   sync.Mutex
	lastNow time.Time
)

// Now returns the canonical timestamp used by every table: UTC RFC3339 with
// microsecond precision, so lexical order equals chronological order.
//
// The wall clock of some hosts advances only every few milliseconds, so two
// writes in a row would otherwise share a timestamp and an update would look
// like it changed nothing. The emitted value is therefore strictly
// increasing: when the clock has not advanced, the previous value is bumped by
// one microsecond.
func Now() string {
	nowMu.Lock()
	defer nowMu.Unlock()
	t := time.Now().UTC()
	if !t.After(lastNow) {
		t = lastNow.Add(time.Microsecond)
	}
	lastNow = t
	return t.Format("2006-01-02T15:04:05.000000Z")
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
```

- [ ] **Step 5: 运行测试确认通过**

```bash
cd D:/work2026/aimemo
go test ./internal/store/
```

Expected: PASS — `ok aimemo/internal/store`

- [ ] **Step 6: 提交**

```bash
cd D:/work2026/aimemo
git add internal/store/
git commit -m "feat: add sqlite store with schema and timestamps"
```

---

### Task 3: CJK 变换与 FTS 查询构建

**Files:**
- Create: `internal/store/fts.go`
- Create: `internal/store/fts_test.go`

- [ ] **Step 1: 写失败的测试**

`internal/store/fts_test.go`：

```go
package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransformCJK(t *testing.T) {
	cases := []struct{ in, want string }{
		{"使用SQLite数据库", "使 用 SQLite 数 据 库"},
		{"Redis作为缓存", "Redis 作 为 缓 存"},
		{"plain ascii", "plain ascii"},
		{"", ""},
	}
	for _, c := range cases {
		if got := TransformCJK(c.in); got != c.want {
			t.Errorf("TransformCJK(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTransformCJKSeparatesLatinFromCJK(t *testing.T) {
	// Without both-side padding "Redis作" stays one token and breaks search.
	got := TransformCJK("使用Redis作为缓存")
	if strings.Contains(got, "Redis作") {
		t.Errorf("TransformCJK left 'Redis作' fused: %q", got)
	}
	if !strings.Contains(got, "Redis 作 为") {
		t.Errorf("TransformCJK did not separate latin from CJK: %q", got)
	}
}

func TestBuildMatch(t *testing.T) {
	got, err := BuildMatch("数据库 SQLite", false)
	if err != nil {
		t.Fatalf("BuildMatch: %v", err)
	}
	want := `"数 据 库" AND "SQLite"`
	if got != want {
		t.Errorf("BuildMatch = %q, want %q", got, want)
	}
}

func TestBuildMatchLoose(t *testing.T) {
	got, err := BuildMatch("数据库", true)
	if err != nil {
		t.Fatalf("BuildMatch: %v", err)
	}
	want := `"数" AND "据" AND "库"`
	if got != want {
		t.Errorf("BuildMatch loose = %q, want %q", got, want)
	}
}

func TestBuildMatchEscapesQuotes(t *testing.T) {
	got, err := BuildMatch(`a"b`, false)
	if err != nil {
		t.Fatalf("BuildMatch: %v", err)
	}
	want := `"a""b"`
	if got != want {
		t.Errorf("BuildMatch = %q, want %q", got, want)
	}
}

func TestBuildMatchRejectsEmpty(t *testing.T) {
	if _, err := BuildMatch("   ", false); err == nil {
		t.Error("BuildMatch should reject an empty query")
	}
	if _, err := BuildMatch("---", false); err == nil {
		t.Error("BuildMatch should reject a query with no searchable tokens")
	}
}

func TestBuildMatchRejectsOversizedQuery(t *testing.T) {
	if _, err := BuildMatch(strings.Repeat("数", MaxQueryRunes), false); err != nil {
		t.Errorf("BuildMatch should accept a query of exactly %d characters: %v", MaxQueryRunes, err)
	}
	if _, err := BuildMatch(strings.Repeat("数", MaxQueryRunes+1), false); err == nil {
		t.Error("BuildMatch should reject an oversized query")
	}
}

func TestTransformCJKRemovesNUL(t *testing.T) {
	if got := TransformCJK("a\x00b"); got != "ab" {
		t.Errorf("TransformCJK(%q) = %q, want %q", "a\x00b", got, "ab")
	}
}

// openFTSStore opens a database for the tests that execute real MATCH queries.
// Task 4 replaces it with the newTestStore helper.
func openFTSStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "aimemo.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestBuildMatchMatchesIndexedText(t *testing.T) {
	s := openFTSStore(t)
	if _, err := s.db.Exec(`INSERT INTO memories_fts(rowid, title, content, tags) VALUES(?,?,?,?)`,
		int64(1), "", TransformCJK("最终选择 SQLite 作为本地持久化数据库"), ""); err != nil {
		t.Fatalf("insert fts: %v", err)
	}

	expr, err := BuildMatch("数据库", false)
	if err != nil {
		t.Fatalf("BuildMatch: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memories_fts WHERE memories_fts MATCH ?`, expr).Scan(&n); err != nil {
		t.Fatalf("match: %v", err)
	}
	if n != 1 {
		t.Errorf("matches = %d, want 1", n)
	}
}

func TestBuildMatchExpressionsAreExecutable(t *testing.T) {
	s := openFTSStore(t)
	queries := []string{
		`a"b`, `*`, `al*`, `NEAR(a b)`, `a OR b`, `-a`, `a:b`, `^a`, `(a)`, `[a]`, `{a}`,
		`!a`, `@a`, `#a`, `$a`, `%a`, `&a`, `+a`, `=a`, `|a`, `\a`, `/a`, `.`, `,`, `?`,
		`<`, `>`, `;`, `~`, "`", `_`, "a\x00b", "数据\x00库", "  ", "!!!",
	}
	executed := 0
	for _, q := range queries {
		expr, err := BuildMatch(q, false)
		if err != nil {
			// Rejecting the query up front is an acceptable outcome too.
			continue
		}
		executed++
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM memories_fts WHERE memories_fts MATCH ?`, expr).Scan(&n); err != nil {
			t.Errorf("query %q produced an invalid expression %q: %v", q, expr, err)
		}
	}
	if executed < 20 {
		t.Fatalf("only %d of %d queries were executed, the corpus no longer exercises MATCH", executed, len(queries))
	}
}

func TestIndexFTSIsIdempotent(t *testing.T) {
	s := openFTSStore(t)
	err := s.withTx(func(tx *sql.Tx) error {
		if err := indexFTS(tx, 1, "标题", "旧内容", []string{"标签"}); err != nil {
			return err
		}
		return indexFTS(tx, 1, "新标题", "新内容", []string{"新标签"})
	})
	if err != nil {
		t.Fatalf("indexFTS: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memories_fts`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("fts rows = %d, want 1", n)
	}
	var content string
	if err := s.db.QueryRow(`SELECT content FROM memories_fts WHERE rowid = 1`).Scan(&content); err != nil {
		t.Fatalf("select: %v", err)
	}
	if content != TransformCJK("新内容") {
		t.Errorf("content = %q, want %q", content, TransformCJK("新内容"))
	}
}

func TestNULIndexAndQueryAgree(t *testing.T) {
	s := openFTSStore(t)
	if _, err := s.db.Exec(`INSERT INTO memories_fts(rowid, title, content, tags) VALUES(?,?,?,?)`,
		int64(1), "", TransformCJK("a\x00b"), ""); err != nil {
		t.Fatalf("insert fts: %v", err)
	}
	expr, err := BuildMatch("a\x00b", false)
	if err != nil {
		t.Fatalf("BuildMatch: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memories_fts WHERE memories_fts MATCH ?`, expr).Scan(&n); err != nil {
		t.Fatalf("match: %v", err)
	}
	if n != 1 {
		t.Errorf("matches = %d, want 1", n)
	}
}

func TestQueryFragments(t *testing.T) {
	got := queryFragments("使用Redis作为缓存")
	want := []string{"使用", "Redis", "作为缓存"}
	if len(got) != len(want) {
		t.Fatalf("queryFragments = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("queryFragments[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd D:/work2026/aimemo
go test ./internal/store/ 2>&1
```

Expected: FAIL — `undefined: TransformCJK`, `undefined: BuildMatch`

- [ ] **Step 3: 创建 internal/store/fts.go**

```go
package store

import (
	"database/sql"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// IsCJK reports whether r is a CJK-style letter that must be tokenized
// character by character for substring search to work.
func IsCJK(r rune) bool {
	return r > 0x2E80 && unicode.IsLetter(r)
}

// TransformCJK pads every CJK letter with spaces on BOTH sides.
//
// The unicode61 tokenizer treats a whole Chinese sentence as a single token,
// so "数据库" would never match inside "使用数据库存储". Padding splits the
// sentence into per-character tokens. Both sides are required: padding only
// after the character would fuse "Redis作" into one token and break searches
// for "redis" in mixed CJK/Latin text.
//
// The padding is collapsed back to single spaces so that the transformed text
// is a clean token sequence and matches what BuildMatch produces.
//
// NUL bytes are dropped. FTS5 tokenizes them as a separator, so keeping them
// in the index but stripping them from the query would make a document and an
// identical query disagree.
func TransformCJK(s string) string {
	var b strings.Builder
	b.Grow(len(s) * 2)
	for _, r := range s {
		if r == 0 {
			continue
		}
		if IsCJK(r) {
			b.WriteRune(' ')
			b.WriteRune(r)
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// segment is a run of either CJK or non-CJK characters.
type segment struct {
	text string
	cjk  bool
}

// splitSegments splits s into alternating CJK / non-CJK runs.
func splitSegments(s string) []segment {
	runes := []rune(s)
	if len(runes) == 0 {
		return nil
	}
	var segs []segment
	cur, start := -1, 0
	for i, r := range runes {
		k := 0
		if IsCJK(r) {
			k = 1
		}
		if cur == -1 {
			cur, start = k, i
			continue
		}
		if k != cur {
			segs = append(segs, segment{text: string(runes[start:i]), cjk: cur == 1})
			cur, start = k, i
		}
	}
	segs = append(segs, segment{text: string(runes[start:]), cjk: cur == 1})
	return segs
}

// hasSearchableToken reports whether s contains at least one letter or digit.
func hasSearchableToken(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// phrase wraps s as an FTS5 quoted phrase with quotes escaped by doubling.
//
// NUL bytes are removed: FTS5 parses the quoted text as a C string, so a NUL
// inside a phrase truncates it and the statement fails with "unterminated
// string". That is the very failure this builder exists to prevent.
func phrase(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// MaxQueryRunes caps the length of a search query. A longer query builds a
// MATCH expression that is expensive for the FTS5 parser to evaluate.
const MaxQueryRunes = 256

// BuildMatch converts a user query into an FTS5 MATCH expression.
//
// The raw user text is never handed to SQLite: every fragment is escaped and
// wrapped, because an unescaped quote makes FTS5 fail with "unterminated
// string".
//
// The query is cut into fragments by whitespace and then by script. A CJK
// fragment becomes one phrase, which FTS5 matches only at consecutive
// positions, i.e. an exact substring: "数据库" matches "本地持久化数据库" but
// not "数据在别处，还有库". A phrase must also lie inside a single column
// (title, content or tags). Fragments are combined with AND, so a query that
// mixes scripts requires all of its fragments to be present. When loose is
// true each CJK character becomes its own term joined by AND, which widens
// recall at the cost of the substring guarantee.
func BuildMatch(query string, loose bool) (string, error) {
	if n := utf8.RuneCountInString(query); n > MaxQueryRunes {
		return "", fmt.Errorf("query is %d characters long, at most %d are allowed", n, MaxQueryRunes)
	}
	fields := strings.Fields(query)
	if len(fields) == 0 {
		return "", fmt.Errorf("empty query")
	}
	var terms []string
	for _, field := range fields {
		for _, seg := range splitSegments(field) {
			if !hasSearchableToken(seg.text) {
				continue
			}
			if seg.cjk {
				if loose {
					for _, r := range seg.text {
						terms = append(terms, phrase(string(r)))
					}
					continue
				}
				terms = append(terms, phrase(TransformCJK(seg.text)))
				continue
			}
			terms = append(terms, phrase(seg.text))
		}
	}
	if len(terms) == 0 {
		return "", fmt.Errorf("query contains no searchable terms")
	}
	return strings.Join(terms, " AND "), nil
}

// queryFragments returns the raw fragments of a query, used to locate a match
// in the original unpadded text.
func queryFragments(query string) []string {
	var out []string
	for _, field := range strings.Fields(query) {
		for _, seg := range splitSegments(field) {
			if seg.text != "" {
				out = append(out, seg.text)
			}
		}
	}
	return out
}

// indexFTS writes or replaces the FTS row for a memory. It must be called
// inside the same transaction as the main table write.
func indexFTS(tx *sql.Tx, id int64, title, content string, tags []string) error {
	if _, err := tx.Exec(`DELETE FROM memories_fts WHERE rowid = ?`, id); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO memories_fts(rowid, title, content, tags) VALUES(?,?,?,?)`,
		id,
		TransformCJK(title),
		TransformCJK(content),
		TransformCJK(strings.Join(tags, " ")),
	)
	return err
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd D:/work2026/aimemo
go test ./internal/store/
```

Expected: PASS

- [ ] **Step 5: 提交**

```bash
cd D:/work2026/aimemo
git add internal/store/
git commit -m "feat: add CJK text transform and FTS match builder"
```

---

### Task 4: 类别与记忆创建 / 读取

**Files:**
- Create: `internal/store/category.go`
- Create: `internal/store/memory.go`
- Create: `internal/store/memory_test.go`

- [ ] **Step 1: 写失败的测试**

`internal/store/memory_test.go`：

```go
package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "aimemo.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCreateAutoCreatesCategory(t *testing.T) {
	s := newTestStore(t)
	m, err := s.Create(CreateInput{
		Category:   "技术选型",
		Type:       "important",
		Importance: 4,
		Title:      "选型结论",
		Content:    "最终选择 SQLite 作为本地持久化数据库",
		Tags:       []string{"db", "选型"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if m.ID == 0 {
		t.Error("expected non-zero id")
	}
	if m.Category != "技术选型" {
		t.Errorf("category = %q", m.Category)
	}
	if m.CreatedAt == "" || m.UpdatedAt == "" {
		t.Error("expected created_at and updated_at to be set")
	}
	if len(m.Tags) != 2 {
		t.Errorf("tags = %v", m.Tags)
	}
}

func TestCreateAppliesDefaults(t *testing.T) {
	s := newTestStore(t)
	m, err := s.Create(CreateInput{Category: "c", Content: "x"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if m.Type != TypeNote {
		t.Errorf("type = %q, want %q", m.Type, TypeNote)
	}
	if m.Importance != 3 {
		t.Errorf("importance = %d, want 3", m.Importance)
	}
}

func TestCreateRejectsDuplicateTags(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Create(CreateInput{Category: "c", Content: "x", Tags: []string{"a", "a"}})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("expected ErrInvalid for a duplicate tag, got %v", err)
	}
}

func TestCreateReusesCategoryIgnoringCase(t *testing.T) {
	s := newTestStore(t)
	a, _ := s.Create(CreateInput{Category: "Tech", Content: "x"})
	b, _ := s.Create(CreateInput{Category: "TECH", Content: "y"})
	if a.CategoryID != b.CategoryID {
		t.Errorf("expected same category, got %d and %d", a.CategoryID, b.CategoryID)
	}
	cats, err := s.ListCategories("name")
	if err != nil {
		t.Fatalf("ListCategories: %v", err)
	}
	if len(cats) != 1 {
		t.Errorf("expected 1 category, got %d", len(cats))
	}
	if cats[0].MemoryCount != 2 {
		t.Errorf("memory_count = %d, want 2", cats[0].MemoryCount)
	}
}

func TestCreateValidatesInput(t *testing.T) {
	s := newTestStore(t)
	cases := []struct {
		name string
		in   CreateInput
	}{
		{"empty category", CreateInput{Content: "x"}},
		{"empty content", CreateInput{Category: "c"}},
		{"bad type", CreateInput{Category: "c", Content: "x", Type: "weird"}},
		{"bad importance", CreateInput{Category: "c", Content: "x", Importance: 9}},
		{"content too long", CreateInput{Category: "c", Content: string(make([]rune, MaxContentLen+1))}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := s.Create(c.in); !errors.Is(err, ErrInvalid) {
				t.Errorf("expected ErrInvalid, got %v", err)
			}
		})
	}
}

func TestGetReturnsFullRecord(t *testing.T) {
	s := newTestStore(t)
	created, _ := s.Create(CreateInput{
		Category: "c", Content: "完整正文内容", Title: "t",
		Type: "summary", Importance: 5, Scope: "proj", Source: "chat",
	})
	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Content != "完整正文内容" {
		t.Errorf("content = %q", got.Content)
	}
	if got.Scope != "proj" || got.Source != "chat" || got.Importance != 5 || got.Type != "summary" {
		t.Errorf("unexpected record: %+v", got)
	}
}

func TestGetNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Get(999); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd D:/work2026/aimemo
go test ./internal/store/ 2>&1
```

Expected: FAIL — `undefined: CreateInput`, `undefined: ErrInvalid`

- [ ] **Step 3: 创建 internal/store/category.go**

```go
package store

import (
	"database/sql"
	"errors"
	"strings"
)

// Category is one AI-defined category.
type Category struct {
	Name        string `json:"name"`
	MemoryCount int    `json:"memory_count"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// ensureCategory returns the id of the category with the given name, creating
// it when it does not exist. Name matching is case-insensitive.
//
// An existing category has its updated_at refreshed, because adding a memory
// to it is a write to the category.
func ensureCategory(tx *sql.Tx, name string) (int64, error) {
	name = strings.TrimSpace(name)
	var id int64
	err := tx.QueryRow(`SELECT id FROM categories WHERE name = ? COLLATE NOCASE`, name).Scan(&id)
	if err == nil {
		if _, err := tx.Exec(`UPDATE categories SET updated_at = ? WHERE id = ?`, Now(), id); err != nil {
			return 0, err
		}
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	now := Now()
	res, err := tx.Exec(`INSERT INTO categories(name, created_at, updated_at) VALUES(?,?,?)`, name, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListCategories returns every category together with its memory count.
func (s *Store) ListCategories(sort string) ([]Category, error) {
	order := `c.name COLLATE NOCASE ASC`
	switch sort {
	case "count":
		order = `COUNT(m.id) DESC, c.name COLLATE NOCASE ASC`
	case "created_at":
		order = `c.created_at DESC, c.id DESC`
	}
	rows, err := s.db.Query(`
		SELECT c.name, COUNT(m.id), c.created_at, c.updated_at
		FROM categories c
		LEFT JOIN memories m ON m.category_id = c.id
		GROUP BY c.id
		ORDER BY ` + order)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Category{}
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.Name, &c.MemoryCount, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: 创建 internal/store/memory.go**

```go
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrInvalid marks caller input that failed validation.
var ErrInvalid = errors.New("invalid input")

// ErrNotFound marks a missing record.
var ErrNotFound = errors.New("not found")

// Field limits.
const (
	MaxContentLen  = 100000
	MaxTitleLen    = 500
	MaxCategoryLen = 100
	MaxTagLen      = 50
	MaxTags        = 20
	MaxScopeLen    = 200
	MaxSourceLen   = 200
)

// Valid memory types.
const (
	TypeSummary   = "summary"
	TypeImportant = "important"
	TypeNote      = "note"
)

func validType(t string) bool {
	return t == TypeSummary || t == TypeImportant || t == TypeNote
}

// Memory is one stored record.
type Memory struct {
	ID           int64    `json:"id"`
	CategoryID   int64    `json:"-"`
	Category     string   `json:"category"`
	Type         string   `json:"type"`
	Importance   int      `json:"importance"`
	Title        string   `json:"title"`
	Content      string   `json:"content"`
	Scope        string   `json:"scope"`
	Source       string   `json:"source"`
	Tags         []string `json:"tags"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	MatchContext string   `json:"match_context,omitempty"`
}

// CreateInput carries the fields of a new record.
type CreateInput struct {
	Category   string
	Type       string
	Importance int
	Title      string
	Content    string
	Scope      string
	Source     string
	Tags       []string
}

// withDefaults fills in the values a caller may leave unset.
func (in CreateInput) withDefaults() CreateInput {
	if in.Type == "" {
		in.Type = TypeNote
	}
	if in.Importance == 0 {
		in.Importance = 3
	}
	return in
}

func (in CreateInput) validate() error {
	if strings.TrimSpace(in.Category) == "" {
		return fmt.Errorf("%w: category must not be empty", ErrInvalid)
	}
	if len([]rune(in.Category)) > MaxCategoryLen {
		return fmt.Errorf("%w: category exceeds %d characters", ErrInvalid, MaxCategoryLen)
	}
	if !validType(in.Type) {
		return fmt.Errorf("%w: type must be one of summary, important, note", ErrInvalid)
	}
	if in.Importance < 1 || in.Importance > 5 {
		return fmt.Errorf("%w: importance must be between 1 and 5", ErrInvalid)
	}
	if strings.TrimSpace(in.Content) == "" {
		return fmt.Errorf("%w: content must not be empty", ErrInvalid)
	}
	if len([]rune(in.Content)) > MaxContentLen {
		return fmt.Errorf("%w: content exceeds %d characters", ErrInvalid, MaxContentLen)
	}
	if len([]rune(in.Title)) > MaxTitleLen {
		return fmt.Errorf("%w: title exceeds %d characters", ErrInvalid, MaxTitleLen)
	}
	if len([]rune(in.Scope)) > MaxScopeLen {
		return fmt.Errorf("%w: scope exceeds %d characters", ErrInvalid, MaxScopeLen)
	}
	if len([]rune(in.Source)) > MaxSourceLen {
		return fmt.Errorf("%w: source exceeds %d characters", ErrInvalid, MaxSourceLen)
	}
	if len(in.Tags) > MaxTags {
		return fmt.Errorf("%w: at most %d tags are allowed", ErrInvalid, MaxTags)
	}
	seen := map[string]bool{}
	for _, t := range in.Tags {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("%w: tag must not be empty", ErrInvalid)
		}
		if len([]rune(t)) > MaxTagLen {
			return fmt.Errorf("%w: tag exceeds %d characters", ErrInvalid, MaxTagLen)
		}
		if seen[t] {
			return fmt.Errorf("%w: duplicate tag %q", ErrInvalid, t)
		}
		seen[t] = true
	}
	return nil
}

// Create stores a new memory, creating its category when needed.
func (s *Store) Create(in CreateInput) (*Memory, error) {
	in = in.withDefaults()
	if err := in.validate(); err != nil {
		return nil, err
	}
	var out *Memory
	err := s.withTx(func(tx *sql.Tx) error {
		catID, err := ensureCategory(tx, in.Category)
		if err != nil {
			return err
		}
		now := Now()
		res, err := tx.Exec(`
			INSERT INTO memories(category_id, type, importance, title, content, scope, source, created_at, updated_at)
			VALUES(?,?,?,?,?,?,?,?,?)`,
			catID, in.Type, in.Importance, in.Title, in.Content, in.Scope, in.Source, now, now)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if err := setTags(tx, id, in.Tags, now); err != nil {
			return err
		}
		if err := indexFTS(tx, id, in.Title, in.Content, in.Tags); err != nil {
			return err
		}
		m, err := getMemory(tx, id)
		if err != nil {
			return err
		}
		out = m
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Get returns one record by id.
func (s *Store) Get(id int64) (*Memory, error) {
	m, err := getMemory(s.db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

// selectMemoryColumns is the standard column list used by every read.
const selectMemoryColumns = `m.id, m.category_id, c.name, m.type, m.importance, m.title, m.content, m.scope, m.source, m.created_at, m.updated_at`

func getMemory(q querier, id int64) (*Memory, error) {
	row := q.QueryRow(`
		SELECT `+selectMemoryColumns+`
		FROM memories m JOIN categories c ON c.id = m.category_id
		WHERE m.id = ?`, id)
	var m Memory
	if err := row.Scan(&m.ID, &m.CategoryID, &m.Category, &m.Type, &m.Importance, &m.Title,
		&m.Content, &m.Scope, &m.Source, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	tags, err := tagsOf(q, m.ID)
	if err != nil {
		return nil, err
	}
	m.Tags = tags
	return &m, nil
}

func setTags(tx *sql.Tx, memoryID int64, tags []string, now string) error {
	if _, err := tx.Exec(`DELETE FROM memory_tags WHERE memory_id = ?`, memoryID); err != nil {
		return err
	}
	for _, t := range tags {
		if _, err := tx.Exec(
			`INSERT INTO memory_tags(memory_id, tag, created_at) VALUES(?,?,?)`,
			memoryID, t, now); err != nil {
			return err
		}
	}
	return nil
}

func tagsOf(q querier, memoryID int64) ([]string, error) {
	rows, err := q.Query(`SELECT tag FROM memory_tags WHERE memory_id = ? ORDER BY tag`, memoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
```

- [ ] **Step 5: 合并测试辅助函数**

`internal/store/fts_test.go` 里的 `openFTSStore` 与 `memory_test.go` 里的
`newTestStore` 完全重复。删掉 `openFTSStore`，把它的四处调用改成 `newTestStore`，
同时移除 `fts_test.go` 中不再使用的 `"path/filepath"` import。

- [ ] **Step 6: 运行测试确认通过**

```bash
cd D:/work2026/aimemo
go test ./internal/store/
```

Expected: PASS

- [ ] **Step 7: 提交**

```bash
cd D:/work2026/aimemo
git add internal/store/
git commit -m "feat: add category and memory create/get with validation"
```

---

### Task 5: 更新与删除

**Files:**
- Modify: `internal/store/memory.go`
- Modify: `internal/store/memory_test.go`

- [ ] **Step 1: 写失败的测试**

追加到 `internal/store/memory_test.go`：

```go
func TestUpdateChangesOnlyGivenFields(t *testing.T) {
	s := newTestStore(t)
	m, _ := s.Create(CreateInput{
		Category: "c", Content: "旧内容", Title: "旧标题",
		Type: "note", Importance: 2, Scope: "old", Source: "old-src",
		Tags: []string{"a", "b"},
	})
	newTitle := "新标题"
	got, err := s.Update(m.ID, UpdateInput{Title: &newTitle})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Title != "新标题" {
		t.Errorf("title = %q", got.Title)
	}
	if got.Content != "旧内容" {
		t.Errorf("content should be untouched, got %q", got.Content)
	}
	if got.Scope != "old" || got.Source != "old-src" {
		t.Errorf("scope/source should be untouched: %+v", got)
	}
	if got.Importance != 2 || got.Type != "note" {
		t.Errorf("type/importance should be untouched: %+v", got)
	}
	if len(got.Tags) != 2 {
		t.Errorf("tags should be untouched, got %v", got.Tags)
	}
	if got.UpdatedAt == got.CreatedAt {
		t.Error("updated_at should have changed")
	}
}

func TestUpdateTags(t *testing.T) {
	s := newTestStore(t)
	m, _ := s.Create(CreateInput{Category: "c", Content: "x", Tags: []string{"a", "b"}})
	got, err := s.Update(m.ID, UpdateInput{AddTags: []string{"c"}, RemoveTags: []string{"a"}})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(got.Tags) != 2 {
		t.Fatalf("tags = %v", got.Tags)
	}
	has := map[string]bool{}
	for _, t := range got.Tags {
		has[t] = true
	}
	if has["a"] {
		t.Error("tag a should have been removed")
	}
	if !has["b"] || !has["c"] {
		t.Errorf("tags = %v", got.Tags)
	}
}

func TestUpdateReindexesFTS(t *testing.T) {
	s := newTestStore(t)
	m, _ := s.Create(CreateInput{Category: "c", Content: "原始内容涉及数据库"})
	newContent := "完全不同的主题"
	if _, err := s.Update(m.ID, UpdateInput{Content: &newContent}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	// The FTS index is queried directly: Search only arrives with Task 6.
	expr, err := BuildMatch("数据库", false)
	if err != nil {
		t.Fatalf("BuildMatch: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memories_fts WHERE memories_fts MATCH ?`, expr).Scan(&n); err != nil {
		t.Fatalf("match: %v", err)
	}
	if n != 0 {
		t.Errorf("old content should no longer match, got %d hits", n)
	}
}

func TestUpdateReplacesTags(t *testing.T) {
	s := newTestStore(t)
	m, _ := s.Create(CreateInput{Category: "c", Content: "x", Tags: []string{"a", "b"}})
	replacement := []string{"z"}
	got, err := s.Update(m.ID, UpdateInput{SetTags: &replacement})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "z" {
		t.Errorf("tags = %v, want [z]", got.Tags)
	}
}

func TestUpdateRejectsDuplicateTags(t *testing.T) {
	s := newTestStore(t)
	m, _ := s.Create(CreateInput{Category: "c", Content: "x", Tags: []string{"a"}})
	replacement := []string{"z", "z"}
	if _, err := s.Update(m.ID, UpdateInput{SetTags: &replacement}); !errors.Is(err, ErrInvalid) {
		t.Errorf("expected ErrInvalid for duplicate tags, got %v", err)
	}
}

func TestUpdateNotFound(t *testing.T) {
	s := newTestStore(t)
	title := "x"
	if _, err := s.Update(999, UpdateInput{Title: &title}); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	m, _ := s.Create(CreateInput{Category: "c", Content: "x", Tags: []string{"t"}})
	if err := s.Delete(m.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(m.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
	if err := s.Delete(m.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound on second delete, got %v", err)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd D:/work2026/aimemo
go test ./internal/store/ 2>&1
```

Expected: FAIL — `undefined: UpdateInput`, `undefined: Filter`, `undefined: s.Search`

- [ ] **Step 3: 在 internal/store/memory.go 末尾追加**

```go
// UpdateInput carries the fields to change. A nil pointer means "keep".
type UpdateInput struct {
	Type       *string
	Importance *int
	Title      *string
	Content    *string
	Category   *string
	Scope      *string
	Source     *string
	SetTags    *[]string
	AddTags    []string
	RemoveTags []string
}

// Update changes a record in place.
func (s *Store) Update(id int64, in UpdateInput) (*Memory, error) {
	var out *Memory
	err := s.withTx(func(tx *sql.Tx) error {
		cur, err := getMemory(tx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		next := UpdateInput{
			Type:       &cur.Type,
			Importance: &cur.Importance,
			Title:      &cur.Title,
			Content:    &cur.Content,
			Scope:      &cur.Scope,
			Source:     &cur.Source,
		}
		if in.Type != nil {
			next.Type = in.Type
		}
		if in.Importance != nil {
			next.Importance = in.Importance
		}
		if in.Title != nil {
			next.Title = in.Title
		}
		if in.Content != nil {
			next.Content = in.Content
		}
		if in.Scope != nil {
			next.Scope = in.Scope
		}
		if in.Source != nil {
			next.Source = in.Source
		}

		catName := cur.Category
		if in.Category != nil {
			catName = *in.Category
		}
		probe := CreateInput{
			Category:   catName,
			Type:       *next.Type,
			Importance: *next.Importance,
			Title:      *next.Title,
			Content:    *next.Content,
			Scope:      *next.Scope,
			Source:     *next.Source,
		}
		if err := probe.validate(); err != nil {
			return err
		}

		catID := cur.CategoryID
		if in.Category != nil {
			catID, err = ensureCategory(tx, *in.Category)
			if err != nil {
				return err
			}
		}

		now := Now()
		if _, err := tx.Exec(`
			UPDATE memories
			SET category_id=?, type=?, importance=?, title=?, content=?, scope=?, source=?, updated_at=?
			WHERE id=?`,
			catID, *next.Type, *next.Importance, *next.Title, *next.Content,
			*next.Scope, *next.Source, now, id); err != nil {
			return err
		}

		tags := cur.Tags
		switch {
		case in.SetTags != nil:
			tags = append([]string{}, *in.SetTags...)
			seen := map[string]bool{}
			for _, t := range tags {
				if strings.TrimSpace(t) == "" {
					return fmt.Errorf("%w: tag must not be empty", ErrInvalid)
				}
				if len([]rune(t)) > MaxTagLen {
					return fmt.Errorf("%w: tag exceeds %d characters", ErrInvalid, MaxTagLen)
				}
				if seen[t] {
					return fmt.Errorf("%w: duplicate tag %q", ErrInvalid, t)
				}
				seen[t] = true
			}
			if len(tags) > MaxTags {
				return fmt.Errorf("%w: at most %d tags are allowed", ErrInvalid, MaxTags)
			}
			if err := setTags(tx, id, tags, now); err != nil {
				return err
			}
		case len(in.AddTags) > 0 || len(in.RemoveTags) > 0:
			remove := map[string]bool{}
			for _, t := range in.RemoveTags {
				remove[t] = true
			}
			kept := []string{}
			present := map[string]bool{}
			for _, t := range tags {
				if remove[t] {
					continue
				}
				kept = append(kept, t)
				present[t] = true
			}
			for _, t := range in.AddTags {
				if strings.TrimSpace(t) == "" {
					return fmt.Errorf("%w: tag must not be empty", ErrInvalid)
				}
				if len([]rune(t)) > MaxTagLen {
					return fmt.Errorf("%w: tag exceeds %d characters", ErrInvalid, MaxTagLen)
				}
				if !present[t] {
					kept = append(kept, t)
					present[t] = true
				}
			}
			if len(kept) > MaxTags {
				return fmt.Errorf("%w: at most %d tags are allowed", ErrInvalid, MaxTags)
			}
			tags = kept
			if err := setTags(tx, id, tags, now); err != nil {
				return err
			}
		}

		if err := indexFTS(tx, id, *next.Title, *next.Content, tags); err != nil {
			return err
		}
		m, err := getMemory(tx, id)
		if err != nil {
			return err
		}
		out = m
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Delete removes a record, its tags and its FTS row.
func (s *Store) Delete(id int64) error {
	return s.withTx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM memories WHERE id = ?`, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(`DELETE FROM memories_fts WHERE rowid = ?`, id); err != nil {
			return err
		}
		return nil
	})
}
```

- [ ] **Step 4: 提交（与 Task 6 一并，Task 6 才能让测试编译通过）**

本任务的测试依赖 Task 6 的 `Filter` 与 `Search`，因此 Task 5 与 Task 6 在同一个提交中落地。直接进入 Task 6。

---

### Task 6: 过滤、列表与搜索

**Files:**
- Create: `internal/store/query.go`
- Modify: `internal/store/memory.go`
- Modify: `internal/store/fts.go`
- Create: `internal/store/query_test.go`

- [ ] **Step 1: 写失败的测试**

`internal/store/query_test.go`：

```go
package store

import (
	"strings"
	"testing"
)

func seed(t *testing.T, s *Store) {
	t.Helper()
	rows := []CreateInput{
		{Category: "技术选型", Type: "important", Importance: 5, Title: "数据库选型",
			Content: "最终选择 SQLite 作为本地持久化数据库", Tags: []string{"db"}},
		{Category: "会议纪要", Type: "summary", Importance: 3, Title: "周会",
			Content: "讨论了 Q4 路线图与人员安排", Scope: "proj-a", Tags: []string{"会议"}},
		{Category: "技术选型", Type: "note", Importance: 2, Title: "缓存",
			Content: "使用 Redis 作为缓存方案", Tags: []string{"cache"}},
	}
	for _, r := range rows {
		if _, err := s.Create(r); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
}

func TestListFilters(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)

	cases := []struct {
		name string
		f    Filter
		want int
	}{
		{"all", Filter{}, 3},
		{"category", Filter{Categories: []string{"技术选型"}}, 2},
		{"scope", Filter{Scopes: []string{"proj-a"}}, 1},
		{"type", Filter{Types: []string{"summary"}}, 1},
		{"tag", Filter{Tags: []string{"db"}}, 1},
		{"min importance", Filter{MinImportance: 3}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := s.List(c.f, 50, 0, "created_at")
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if res.Total != c.want {
				t.Errorf("total = %d, want %d", res.Total, c.want)
			}
		})
	}
}

func TestListSortByImportance(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)
	res, err := s.List(Filter{}, 50, 0, "importance")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if res.Items[0].Importance != 5 {
		t.Errorf("first item importance = %d, want 5", res.Items[0].Importance)
	}
}

func TestSearchChineseSubstring(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)
	res, err := s.Search("数据库", Filter{}, 50, 0, "importance", false)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Total != 1 {
		t.Fatalf("total = %d, want 1 (hits %+v)", res.Total, res.Items)
	}
	if !strings.Contains(res.Items[0].Content, "数据库") {
		t.Errorf("unexpected hit: %+v", res.Items[0])
	}
}

func TestSearchChineseSubstringIsExact(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)
	res, err := s.Search("数据库", Filter{}, 50, 0, "importance", false)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, it := range res.Items {
		if strings.Contains(it.Content, "使用数据") && !strings.Contains(it.Content, "数据库") {
			t.Errorf("substring search matched a non-substring: %q", it.Content)
		}
	}
}

func TestSearchMixedCJKAndLatin(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)
	res, err := s.Search("redis", Filter{}, 50, 0, "importance", false)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Total != 1 {
		t.Fatalf("total = %d, want 1", res.Total)
	}
	if !strings.Contains(res.Items[0].Content, "Redis") {
		t.Errorf("unexpected hit: %+v", res.Items[0])
	}
}

func TestSearchLatinWordInsideCJKText(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)
	res, err := s.Search("SQLite", Filter{}, 50, 0, "importance", false)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Total != 1 {
		t.Fatalf("total = %d, want 1", res.Total)
	}
}

func TestSearchLooseMode(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create(CreateInput{Category: "c", Content: "这里只有数据没有库"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	strict, err := s.Search("数据库", Filter{}, 50, 0, "importance", false)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	loose, err := s.Search("数据库", Filter{}, 50, 0, "importance", true)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if strict.Total != 0 {
		t.Errorf("strict total = %d, want 0", strict.Total)
	}
	if loose.Total != 1 {
		t.Errorf("loose total = %d, want 1", loose.Total)
	}
}

func TestSearchMatchContextUsesOriginalText(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create(CreateInput{
		Category: "c",
		Content:  "前置说明：最终选择 SQLite 作为数据库持久化方案，后置说明",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	res, err := s.Search("数据库", Filter{}, 50, 0, "importance", false)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(res.Items))
	}
	mc := res.Items[0].MatchContext
	if mc == "" {
		t.Fatal("match_context is empty")
	}
	if strings.Contains(mc, "数 据 库") {
		t.Errorf("match_context contains padded text: %q", mc)
	}
	if !strings.Contains(mc, "数据库") {
		t.Errorf("match_context does not contain the term: %q", mc)
	}
}

func TestSearchCombinedWithFilters(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)
	res, err := s.Search("选型", Filter{Categories: []string{"会议纪要"}}, 50, 0, "importance", false)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Total != 0 {
		t.Errorf("total = %d, want 0", res.Total)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd D:/work2026/aimemo
go test ./internal/store/ 2>&1
```

Expected: FAIL — `undefined: Filter`, `undefined: s.List`, `undefined: s.Search`

- [ ] **Step 3: 创建 internal/store/query.go**

```go
package store

import (
	"database/sql"
	"strings"
)

// Filter narrows a listing or a search. Repeated values inside one field are
// combined with OR.
type Filter struct {
	Categories    []string
	Scopes        []string
	Types         []string
	Tags          []string
	MinImportance int
	Since         string
	Until         string
}

// clauses renders the filter as a SQL predicate plus its bind arguments.
func (f Filter) clauses() (string, []any) {
	var cs []string
	var args []any

	if len(f.Categories) > 0 {
		ph := make([]string, 0, len(f.Categories))
		for _, c := range f.Categories {
			ph = append(ph, "c.name = ? COLLATE NOCASE")
			args = append(args, c)
		}
		cs = append(cs, "("+strings.Join(ph, " OR ")+")")
	}
	if len(f.Scopes) > 0 {
		ph := make([]string, 0, len(f.Scopes))
		for _, sc := range f.Scopes {
			ph = append(ph, "m.scope = ?")
			args = append(args, sc)
		}
		cs = append(cs, "("+strings.Join(ph, " OR ")+")")
	}
	if len(f.Types) > 0 {
		ph := make([]string, 0, len(f.Types))
		for _, t := range f.Types {
			ph = append(ph, "m.type = ?")
			args = append(args, t)
		}
		cs = append(cs, "("+strings.Join(ph, " OR ")+")")
	}
	if len(f.Tags) > 0 {
		ph := make([]string, 0, len(f.Tags))
		for _, t := range f.Tags {
			ph = append(ph, "?")
			args = append(args, t)
		}
		cs = append(cs, "m.id IN (SELECT memory_id FROM memory_tags WHERE tag IN ("+strings.Join(ph, ",")+"))")
	}
	if f.MinImportance > 0 {
		cs = append(cs, "m.importance >= ?")
		args = append(args, f.MinImportance)
	}
	if f.Since != "" {
		cs = append(cs, "m.created_at >= ?")
		args = append(args, f.Since)
	}
	if f.Until != "" {
		cs = append(cs, "m.created_at <= ?")
		args = append(args, f.Until)
	}
	if len(cs) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(cs, " AND "), args
}

// orderBy renders the ORDER BY clause. relevance only applies to a search,
// because bm25 needs the FTS table in the query.
func orderBy(sort string, searchable bool) string {
	switch sort {
	case "importance":
		return "ORDER BY m.importance DESC, m.created_at DESC, m.id DESC"
	case "relevance":
		if searchable {
			return "ORDER BY bm25(memories_fts), m.created_at DESC, m.id DESC"
		}
	case "created_at", "":
	}
	return "ORDER BY m.created_at DESC, m.id DESC"
}

// attachTags loads the tags of many records in one query.
func attachTags(q querier, mems []Memory) error {
	if len(mems) == 0 {
		return nil
	}
	ph := make([]string, 0, len(mems))
	args := make([]any, 0, len(mems))
	byID := make(map[int64]*Memory, len(mems))
	for i := range mems {
		mems[i].Tags = []string{}
		ph = append(ph, "?")
		args = append(args, mems[i].ID)
		byID[mems[i].ID] = &mems[i]
	}
	rows, err := q.Query(
		`SELECT memory_id, tag FROM memory_tags WHERE memory_id IN (`+strings.Join(ph, ",")+`) ORDER BY tag`,
		args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var tag string
		if err := rows.Scan(&id, &tag); err != nil {
			return err
		}
		if m, ok := byID[id]; ok {
			m.Tags = append(m.Tags, tag)
		}
	}
	return rows.Err()
}

// scanMemories reads the standard memory column set.
func scanMemories(rows *sql.Rows) ([]Memory, error) {
	defer rows.Close()
	out := []Memory{}
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.CategoryID, &m.Category, &m.Type, &m.Importance,
			&m.Title, &m.Content, &m.Scope, &m.Source, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: 在 internal/store/memory.go 末尾追加 List 与 Search**

```go
// ListResult is a page of memories.
type ListResult struct {
	Items  []Memory `json:"items"`
	Total  int      `json:"total"`
	Limit  int      `json:"limit"`
	Offset int      `json:"offset"`
}

// SearchResult is a page of matches for a keyword query.
type SearchResult struct {
	Items []Memory `json:"items"`
	Total int      `json:"total"`
	Query string   `json:"query"`
}

// List returns memories matching the filter, newest first by default.
func (s *Store) List(f Filter, limit, offset int, sort string) (*ListResult, error) {
	where, args := f.clauses()

	var total int
	if err := s.db.QueryRow(`
		SELECT COUNT(*)
		FROM memories m JOIN categories c ON c.id = m.category_id
		WHERE 1=1`+where, args...).Scan(&total); err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`
		SELECT `+selectMemoryColumns+`
		FROM memories m JOIN categories c ON c.id = m.category_id
		WHERE 1=1`+where+` `+orderBy(sort, false)+` LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	items, err := scanMemories(rows)
	if err != nil {
		return nil, err
	}
	if err := attachTags(s.db, items); err != nil {
		return nil, err
	}
	return &ListResult{Items: items, Total: total, Limit: limit, Offset: offset}, nil
}

// Search performs a full text search and returns matching memories with a
// window of surrounding text.
func (s *Store) Search(query string, f Filter, limit, offset int, sort string, loose bool) (*SearchResult, error) {
	expr, err := BuildMatch(query, loose)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	where, args := f.clauses()
	args = append([]any{expr}, args...)

	var total int
	if err := s.db.QueryRow(`
		SELECT COUNT(*)
		FROM memories_fts
		JOIN memories m ON m.id = memories_fts.rowid
		JOIN categories c ON c.id = m.category_id
		WHERE memories_fts MATCH ?`+where, args...).Scan(&total); err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`
		SELECT `+selectMemoryColumns+`
		FROM memories_fts
		JOIN memories m ON m.id = memories_fts.rowid
		JOIN categories c ON c.id = m.category_id
		WHERE memories_fts MATCH ?`+where+` `+orderBy(sort, true)+` LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	items, err := scanMemories(rows)
	if err != nil {
		return nil, err
	}
	if err := attachTags(s.db, items); err != nil {
		return nil, err
	}
	for i := range items {
		items[i].MatchContext = MatchContext(items[i].Content, query, 120)
	}
	return &SearchResult{Items: items, Total: total, Query: query}, nil
}
```

- [ ] **Step 5: 在 internal/store/fts.go 末尾追加 MatchContext**

```go
// MatchContext returns a window of the original text around the longest query
// fragment that occurs in it. The window is computed on the unpadded text, so
// the result never contains the artificial spaces inserted for tokenization.
func MatchContext(content, query string, window int) string {
	hr := []rune(content)
	fragments := queryFragments(query)

	bestStart, bestLen := -1, 0
	for _, f := range fragments {
		fr := []rune(f)
		if len(fr) == 0 {
			continue
		}
		if idx := indexFoldRunes(hr, fr); idx >= 0 && len(fr) > bestLen {
			bestStart, bestLen = idx, len(fr)
		}
	}
	if bestStart < 0 {
		end := window * 2
		if end > len(hr) {
			end = len(hr)
		}
		out := string(hr[:end])
		if end < len(hr) {
			out += "…"
		}
		return out
	}

	start := bestStart - window
	if start < 0 {
		start = 0
	}
	end := bestStart + bestLen + window
	if end > len(hr) {
		end = len(hr)
	}
	out := string(hr[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(hr) {
		out += "…"
	}
	return out
}

// indexFoldRunes returns the index of needle inside haystack, comparing runes
// case-insensitively.
func indexFoldRunes(haystack, needle []rune) int {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return -1
	}
	lower := make([]rune, len(haystack))
	for i, r := range haystack {
		lower[i] = unicode.ToLower(r)
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		ok := true
		for j := range needle {
			if lower[i+j] != unicode.ToLower(needle[j]) {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 6: 运行测试确认通过**

```bash
cd D:/work2026/aimemo
go test ./internal/store/
```

Expected: PASS — 全部通过，包含 Task 5 挂起的 `TestUpdateReindexesFTS`

- [ ] **Step 7: 提交**

```bash
cd D:/work2026/aimemo
git add internal/store/
git commit -m "feat: add filters, listing, full text search and match context"
```

---

### Task 7: CLI 骨架（信封、错误码、退出码、全局 flag）

**Files:**
- Create: `internal/cli/root.go`
- Create: `internal/cli/render.go`
- Create: `internal/cli/commands.go`（命令桩，Task 8 覆盖）
- Modify: `main.go`
- Create: `internal/cli/root_test.go`

- [ ] **Step 1: 写失败的测试**

`internal/cli/root_test.go`：

```go
package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func newDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "aimemo.db")
}

// run invokes the CLI and returns stdout, stderr and the exit code.
func run(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(args, &out, &errb)
	return out.String(), errb.String(), code
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return m
}

func TestUnknownCommandIsUsageError(t *testing.T) {
	_, stderr, code := run(t, "nope")
	if code != exitUsage {
		t.Errorf("code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "unknown command") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestNoCommandIsUsageError(t *testing.T) {
	_, _, code := run(t)
	if code != exitUsage {
		t.Errorf("code = %d, want %d", code, exitUsage)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd D:/work2026/aimemo
go test ./internal/cli/ 2>&1
```

Expected: FAIL — `undefined: Run`

- [ ] **Step 3: 创建 internal/cli/root.go**

```go
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"aimemo/internal/store"
)

// Process exit codes.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// Machine readable error codes.
const (
	codeInvalidArgs = "invalid_args"
	codeNotFound    = "not_found"
	codeConflict    = "conflict"
	codeDBError     = "db_error"
	codeIOError     = "io_error"
)

// appError carries a machine readable code alongside a human message.
type appError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *appError) Error() string { return e.Message }

func newError(code, format string, args ...any) *appError {
	return &appError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// envelope is the stable output contract consumed by the AI.
type envelope struct {
	OK    bool      `json:"ok"`
	Data  any       `json:"data,omitempty"`
	Error *appError `json:"error,omitempty"`
}

// globalOpts holds options valid for every command.
type globalOpts struct {
	dbPath string
	format string
}

// commandNames lists the supported subcommands.
var commandNames = []string{
	"add", "list", "search", "get", "update", "delete", "categories", "stats", "usage",
}

// Run executes one invocation and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	g, rest := extractGlobals(args)
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "aimemo: a command is required")
		printCommandList(stderr)
		return exitUsage
	}
	cmd, cmdArgs := rest[0], rest[1:]
	switch cmd {
	case "add":
		return runAdd(g, cmdArgs, stdout, stderr)
	case "list":
		return runList(g, cmdArgs, stdout, stderr)
	case "search":
		return runSearch(g, cmdArgs, stdout, stderr)
	case "get":
		return runGet(g, cmdArgs, stdout, stderr)
	case "update":
		return runUpdate(g, cmdArgs, stdout, stderr)
	case "delete":
		return runDelete(g, cmdArgs, stdout, stderr)
	case "categories":
		return runCategories(g, cmdArgs, stdout, stderr)
	case "stats":
		return runStats(g, cmdArgs, stdout, stderr)
	case "usage":
		return runUsage(g, cmdArgs, stdout, stderr)
	case "help", "-h", "--help":
		printCommandList(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "aimemo: unknown command %q\n", cmd)
		printCommandList(stderr)
		return exitUsage
	}
}

// extractGlobals pulls --db and --format out of the argument list so they may
// appear before or after the subcommand.
func extractGlobals(args []string) (globalOpts, []string) {
	g := globalOpts{dbPath: defaultDBPath(), format: "json"}
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--db" && i+1 < len(args):
			g.dbPath = args[i+1]
			i++
		case strings.HasPrefix(a, "--db="):
			g.dbPath = strings.TrimPrefix(a, "--db=")
		case a == "--format" && i+1 < len(args):
			g.format = args[i+1]
			i++
		case strings.HasPrefix(a, "--format="):
			g.format = strings.TrimPrefix(a, "--format=")
		default:
			rest = append(rest, a)
		}
	}
	return g, rest
}

// defaultDBPath honours AIMEMO_DB, otherwise uses ~/.aimemo/aimemo.db.
func defaultDBPath() string {
	if p := os.Getenv("AIMEMO_DB"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "aimemo.db"
	}
	return filepath.Join(home, ".aimemo", "aimemo.db")
}

// openStore opens the database for a command.
func openStore(g globalOpts) (*store.Store, error) {
	s, err := store.Open(g.dbPath)
	if err != nil {
		return nil, newError(codeIOError, "open database: %v", err)
	}
	return s, nil
}

// fail reports an error on stderr and returns the exit code.
func fail(g globalOpts, stderr io.Writer, err error) int {
	code := codeDBError
	var ae *appError
	switch {
	case errors.As(err, &ae):
		code = ae.Code
	case errors.Is(err, store.ErrInvalid):
		code = codeInvalidArgs
	case errors.Is(err, store.ErrNotFound):
		code = codeNotFound
	}
	if g.format == "text" {
		fmt.Fprintf(stderr, "aimemo: %v\n", err)
	} else {
		b, merr := json.Marshal(envelope{OK: false, Error: &appError{Code: code, Message: err.Error()}})
		if merr != nil {
			fmt.Fprintf(stderr, "{\"ok\":false,\"error\":{\"code\":%q,\"message\":%q}}\n", code, err.Error())
		} else {
			fmt.Fprintf(stderr, "%s\n", b)
		}
	}
	if code == codeInvalidArgs {
		return exitUsage
	}
	return exitError
}

// printCommandList writes the supported commands.
func printCommandList(w io.Writer) {
	fmt.Fprintln(w, "usage: aimemo [--db PATH] [--format json|text] <command> [options]")
	fmt.Fprintln(w, "commands:")
	for _, c := range commandNames {
		fmt.Fprintf(w, "  %s\n", c)
	}
	fmt.Fprintln(w, "  help")
	fmt.Fprintln(w, "run 'aimemo usage' for the full AI facing guide")
}

// stringList collects a repeatable string flag.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// isBoolFlag reports whether a flag takes no value.
func isBoolFlag(f *flag.Flag) bool {
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}

// splitArgs separates positional arguments from flags so that positionals may
// appear before or after flags. Go's flag package stops at the first
// positional, which would break "aimemo search 数据库 --limit 5".
func splitArgs(fs *flag.FlagSet, args []string) (positional []string, flagArgs []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' {
			name := strings.TrimLeft(a, "-")
			if strings.Contains(name, "=") {
				flagArgs = append(flagArgs, a)
				continue
			}
			f := fs.Lookup(name)
			if f != nil && !isBoolFlag(f) && i+1 < len(args) {
				flagArgs = append(flagArgs, a, args[i+1])
				i++
				continue
			}
			flagArgs = append(flagArgs, a)
			continue
		}
		positional = append(positional, a)
	}
	return positional, flagArgs
}

// parseFlagSet parses args into fs, tolerating positionals in any position.
// It returns the positional arguments.
func parseFlagSet(fs *flag.FlagSet, args []string, stdout io.Writer) ([]string, error) {
	fs.SetOutput(io.Discard)
	positional, flagArgs := splitArgs(fs, args)
	if err := fs.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.SetOutput(stdout)
			fs.Usage()
			return positional, errHelpRequested
		}
		return positional, err
	}
	return positional, nil
}

// errHelpRequested signals that the user asked for the command help.
var errHelpRequested = errors.New("help requested")
```

- [ ] **Step 4: 创建 internal/cli/render.go**

```go
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"aimemo/internal/store"
)

// memoryView is the CLI facing shape of a stored record.
type memoryView struct {
	ID             int64    `json:"id"`
	Category       string   `json:"category"`
	Type           string   `json:"type"`
	Importance     int      `json:"importance"`
	Title          string   `json:"title"`
	Content        string   `json:"content,omitempty"`
	ContentPreview string   `json:"content_preview,omitempty"`
	Scope          string   `json:"scope"`
	Source         string   `json:"source"`
	Tags           []string `json:"tags"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
	MatchContext   string   `json:"match_context,omitempty"`
}

// previewLimit is the number of characters kept in content_preview.
const previewLimit = 200

// toMemoryView renders a full record.
func toMemoryView(m store.Memory) memoryView {
	return memoryView{
		ID:           m.ID,
		Category:     m.Category,
		Type:         m.Type,
		Importance:   m.Importance,
		Title:        m.Title,
		Content:      m.Content,
		Scope:        m.Scope,
		Source:       m.Source,
		Tags:         m.Tags,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
		MatchContext: m.MatchContext,
	}
}

// toMemoryPreview renders a record without the full body.
func toMemoryPreview(m store.Memory) memoryView {
	v := toMemoryView(m)
	v.Content = ""
	r := []rune(m.Content)
	if len(r) > previewLimit {
		v.ContentPreview = string(r[:previewLimit]) + "…"
	} else {
		v.ContentPreview = m.Content
	}
	return v
}

// emit writes the success envelope to w.
func emit(g globalOpts, w io.Writer, data any) {
	if g.format == "text" {
		writeText(w, data)
		return
	}
	b, err := json.MarshalIndent(envelope{OK: true, Data: data}, "", "  ")
	if err != nil {
		fmt.Fprintf(w, "{\"ok\":false,\"error\":{\"code\":%q,\"message\":%q}}\n", codeDBError, err.Error())
		return
	}
	fmt.Fprintf(w, "%s\n", b)
}

// writeText renders the command output as plain text.
func writeText(w io.Writer, data any) {
	switch v := data.(type) {
	case memoryView:
		writeMemoryText(w, v)
	case []memoryView:
		for _, m := range v {
			writeMemoryText(w, m)
		}
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sortStrings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "%s: %v\n", k, v[k])
		}
	default:
		b, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			fmt.Fprintf(w, "%v\n", data)
			return
		}
		fmt.Fprintf(w, "%s\n", b)
	}
}

// writeMemoryText renders one memory record as plain text.
func writeMemoryText(w io.Writer, m memoryView) {
	fmt.Fprintf(w, "#%d  %s  (%s, importance %d)\n", m.ID, m.Category, m.Type, m.Importance)
	if m.Title != "" {
		fmt.Fprintf(w, "  title: %s\n", m.Title)
	}
	if len(m.Tags) > 0 {
		fmt.Fprintf(w, "  tags: %s\n", strings.Join(m.Tags, ", "))
	}
	if m.Scope != "" {
		fmt.Fprintf(w, "  scope: %s\n", m.Scope)
	}
	if m.Source != "" {
		fmt.Fprintf(w, "  source: %s\n", m.Source)
	}
	if m.ContentPreview != "" {
		fmt.Fprintf(w, "  %s\n", m.ContentPreview)
	}
	if m.Content != "" {
		fmt.Fprintf(w, "  %s\n", m.Content)
	}
	if m.MatchContext != "" {
		fmt.Fprintf(w, "  match: %s\n", m.MatchContext)
	}
	fmt.Fprintf(w, "  created: %s  updated: %s\n", m.CreatedAt, m.UpdatedAt)
}

// sortStrings sorts a slice of strings in place.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
```

- [ ] **Step 5: 创建 internal/cli/commands.go（命令桩）**

Task 8 会把这里的桩替换成真正的实现，所以桩单独放在 `commands.go` 里，
而不是追加到 `root.go` 末尾——否则 Task 8 会产生重复定义。

```go
package cli

import "io"

// The command stubs are replaced by the real implementations in Task 8.
func runAdd(g globalOpts, args []string, stdout, stderr io.Writer) int       { return exitUsage }
func runList(g globalOpts, args []string, stdout, stderr io.Writer) int      { return exitUsage }
func runSearch(g globalOpts, args []string, stdout, stderr io.Writer) int    { return exitUsage }
func runGet(g globalOpts, args []string, stdout, stderr io.Writer) int       { return exitUsage }
func runUpdate(g globalOpts, args []string, stdout, stderr io.Writer) int    { return exitUsage }
func runDelete(g globalOpts, args []string, stdout, stderr io.Writer) int    { return exitUsage }
func runCategories(g globalOpts, args []string, stdout, stderr io.Writer) int { return exitUsage }
func runStats(g globalOpts, args []string, stdout, stderr io.Writer) int     { return exitUsage }
func runUsage(g globalOpts, args []string, stdout, stderr io.Writer) int      { return exitUsage }
```

- [ ] **Step 6: 写 main.go**

用以下内容替换 `main.go`：

```go
// aimemo is a memory store for AI agents.
package main

import (
	"os"

	"aimemo/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
```

- [ ] **Step 7: 运行测试确认通过**

```bash
cd D:/work2026/aimemo
go test ./internal/cli/
```

Expected: PASS

- [ ] **Step 8: 提交**

```bash
cd D:/work2026/aimemo
git add internal/cli/ main.go
git commit -m "feat: add cli skeleton with json envelope and exit codes"
```

---

### Task 8: 九个子命令的实现

**Files:**
- Modify: `internal/cli/commands.go`（整体替换命令桩）
- Modify: `internal/store/category.go`（新增 `Stats`）

- [ ] **Step 1: 在 internal/store/category.go 末尾追加 Stats**

```go
// Stats summarises the stored memories.
type Stats struct {
	Total             int            `json:"total"`
	CategoryCount     int            `json:"category_count"`
	ByCategory        map[string]int `json:"by_category"`
	ByType            map[string]int `json:"by_type"`
	ByScope           map[string]int `json:"by_scope"`
	AverageImportance float64        `json:"average_importance"`
}

// Stats returns a summary of the store.
func (s *Store) Stats() (*Stats, error) {
	out := &Stats{
		ByCategory: map[string]int{},
		ByType:     map[string]int{},
		ByScope:    map[string]int{},
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&out.Total); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM categories`).Scan(&out.CategoryCount); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow(`SELECT COALESCE(AVG(importance), 0) FROM memories`).Scan(&out.AverageImportance); err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`SELECT c.name, COUNT(m.id) FROM categories c LEFT JOIN memories m ON m.category_id = c.id GROUP BY c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out.ByCategory[name] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	trows, err := s.db.Query(`SELECT type, COUNT(*) FROM memories GROUP BY type`)
	if err != nil {
		return nil, err
	}
	defer trows.Close()
	for trows.Next() {
		var name string
		var n int
		if err := trows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out.ByType[name] = n
	}
	if err := trows.Err(); err != nil {
		return nil, err
	}

	srows, err := s.db.Query(`SELECT scope, COUNT(*) FROM memories GROUP BY scope`)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	for srows.Next() {
		var name string
		var n int
		if err := srows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out.ByScope[name] = n
	}
	return out, srows.Err()
}
```

- [ ] **Step 2: 用完整实现替换 internal/cli/commands.go（覆盖 Task 7 写入的命令桩）**

整个文件替换，不是追加：Task 7 写入的九个命令桩必须被覆盖掉，否则会产生重复定义。

```go
package cli

import (
	"errors"
	"flag"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"aimemo/internal/store"
)

// listOutput is the payload of the list command.
type listOutput struct {
	Items  []memoryView `json:"items"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

// searchOutput is the payload of the search command.
type searchOutput struct {
	Items []memoryView `json:"items"`
	Total int          `json:"total"`
	Query string       `json:"query"`
}

// categoryView is the payload of one category.
type categoryView struct {
	Name        string `json:"name"`
	MemoryCount int    `json:"memory_count"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// deleteOutput is the payload of the delete command.
type deleteOutput struct {
	Deleted int64 `json:"deleted"`
}

// filterFlags holds the filter options shared by list and search.
type filterFlags struct {
	categories    stringList
	scopes        stringList
	types         stringList
	tags          stringList
	minImportance int
	since         string
	until         string
	limit         int
	offset        int
	sort          string
}

// registerFilterFlags declares the shared filter options.
func registerFilterFlags(fs *flag.FlagSet) *filterFlags {
	f := &filterFlags{}
	fs.Var(&f.categories, "category", "filter by category, repeatable (OR)")
	fs.Var(&f.scopes, "scope", "filter by scope, repeatable (OR)")
	fs.Var(&f.types, "type", "filter by type: summary|important|note, repeatable (OR)")
	fs.Var(&f.tags, "tag", "filter by tag, repeatable (OR)")
	fs.IntVar(&f.minImportance, "min-importance", 0, "minimum importance, 1-5")
	fs.StringVar(&f.since, "since", "", "created at or after, RFC3339 or YYYY-MM-DD")
	fs.StringVar(&f.until, "until", "", "created at or before, RFC3339 or YYYY-MM-DD")
	fs.IntVar(&f.limit, "limit", 20, "maximum results, 1-500")
	fs.IntVar(&f.offset, "offset", 0, "result offset")
	fs.StringVar(&f.sort, "sort", "", "sort: created_at | importance | relevance")
	return f
}

// toFilter validates the filter options and builds a store.Filter.
func (f *filterFlags) toFilter() (store.Filter, error) {
	if f.limit < 1 || f.limit > 500 {
		return store.Filter{}, newError(codeInvalidArgs, "limit must be between 1 and 500")
	}
	if f.offset < 0 {
		return store.Filter{}, newError(codeInvalidArgs, "offset must not be negative")
	}
	if f.minImportance < 0 || f.minImportance > 5 {
		return store.Filter{}, newError(codeInvalidArgs, "min-importance must be between 1 and 5")
	}
	for _, t := range f.types {
		if t != store.TypeSummary && t != store.TypeImportant && t != store.TypeNote {
			return store.Filter{}, newError(codeInvalidArgs, "type must be summary, important or note")
		}
	}
	switch f.sort {
	case "", "created_at", "importance", "relevance":
	default:
		return store.Filter{}, newError(codeInvalidArgs, "sort must be created_at, importance or relevance")
	}
	since, err := parseBoundary(f.since, false)
	if err != nil {
		return store.Filter{}, err
	}
	until, err := parseBoundary(f.until, true)
	if err != nil {
		return store.Filter{}, err
	}
	return store.Filter{
		Categories:    f.categories,
		Scopes:        f.scopes,
		Types:         f.types,
		Tags:          f.tags,
		MinImportance: f.minImportance,
		Since:         since,
		Until:         until,
	}, nil
}

// parseBoundary converts a date or timestamp into the canonical UTC format
// used by created_at. endOfDay expands a plain date to the end of that day.
func parseBoundary(v string, endOfDay bool) (string, error) {
	if strings.TrimSpace(v) == "" {
		return "", nil
	}
	if len(v) == 10 && v[4] == '-' && v[7] == '-' {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return "", newError(codeInvalidArgs, "invalid date %q", v)
		}
		if endOfDay {
			t = t.Add(24*time.Hour - time.Millisecond)
		}
		return t.UTC().Format("2006-01-02T15:04:05.000000Z"), nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return "", newError(codeInvalidArgs, "invalid timestamp %q, expected RFC3339 or YYYY-MM-DD", v)
	}
	return t.UTC().Format("2006-01-02T15:04:05.000000Z"), nil
}

// parseID parses a record id.
func parseID(s string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n <= 0 {
		return 0, newError(codeInvalidArgs, "invalid id %q", s)
	}
	return n, nil
}

// runAdd stores a new memory.
func runAdd(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	category := fs.String("category", "", "category name, created automatically")
	content := fs.String("content", "", "body text, use - to read from stdin")
	title := fs.String("title", "", "optional title")
	typ := fs.String("type", store.TypeNote, "summary | important | note")
	importance := fs.Int("importance", 3, "importance from 1 to 5")
	var tags stringList
	fs.Var(&tags, "tag", "tag, repeatable")
	scope := fs.String("scope", "", "optional scope, for example a project name")
	source := fs.String("source", "", "optional source")

	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}

	body := *content
	if body == "-" {
		b, rerr := io.ReadAll(os.Stdin)
		if rerr != nil {
			return fail(g, stderr, newError(codeIOError, "read stdin: %v", rerr))
		}
		body = string(b)
	}

	if *importance < 1 || *importance > 5 {
		return fail(g, stderr, newError(codeInvalidArgs, "importance must be between 1 and 5"))
	}
	if *typ != store.TypeSummary && *typ != store.TypeImportant && *typ != store.TypeNote {
		return fail(g, stderr, newError(codeInvalidArgs, "type must be summary, important or note"))
	}

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	m, err := s.Create(store.CreateInput{
		Category:   *category,
		Type:       *typ,
		Importance: *importance,
		Title:      *title,
		Content:    body,
		Scope:      *scope,
		Source:     *source,
		Tags:       tags,
	})
	if err != nil {
		return fail(g, stderr, err)
	}
	emit(g, stdout, toMemoryView(*m))
	return exitOK
}

// runList returns a filtered page of memories.
func runList(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	ff := registerFilterFlags(fs)

	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	f, err := ff.toFilter()
	if err != nil {
		return fail(g, stderr, err)
	}

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	sort := ff.sort
	if sort == "" {
		sort = "created_at"
	}
	res, err := s.List(f, ff.limit, ff.offset, sort)
	if err != nil {
		return fail(g, stderr, err)
	}
	items := make([]memoryView, 0, len(res.Items))
	for _, m := range res.Items {
		items = append(items, toMemoryPreview(m))
	}
	emit(g, stdout, listOutput{Items: items, Total: res.Total, Limit: ff.limit, Offset: ff.offset})
	return exitOK
}

// runSearch performs a full text search.
func runSearch(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	ff := registerFilterFlags(fs)
	loose := fs.Bool("loose", false, "relax CJK phrase matching to per character AND")

	positional, err := parseFlagSet(fs, args, stdout)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	query := strings.TrimSpace(strings.Join(positional, " "))
	if query == "" {
		return fail(g, stderr, newError(codeInvalidArgs, "search requires a query"))
	}
	f, err := ff.toFilter()
	if err != nil {
		return fail(g, stderr, err)
	}

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	sort := ff.sort
	if sort == "" {
		sort = "importance"
	}
	res, err := s.Search(query, f, ff.limit, ff.offset, sort, *loose)
	if err != nil {
		return fail(g, stderr, err)
	}
	items := make([]memoryView, 0, len(res.Items))
	for _, m := range res.Items {
		items = append(items, toMemoryView(m))
	}
	emit(g, stdout, searchOutput{Items: items, Total: res.Total, Query: res.Query})
	return exitOK
}

// runGet returns one record.
func runGet(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	positional, err := parseFlagSet(fs, args, stdout)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	if len(positional) < 1 {
		return fail(g, stderr, newError(codeInvalidArgs, "get requires an id"))
	}
	id, err := parseID(positional[0])
	if err != nil {
		return fail(g, stderr, err)
	}

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	m, err := s.Get(id)
	if err != nil {
		return fail(g, stderr, err)
	}
	emit(g, stdout, toMemoryView(*m))
	return exitOK
}

// runUpdate changes a record. Only flags that were explicitly given are
// applied, which is detected with flag.Visit.
func runUpdate(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	category := fs.String("category", "", "new category name")
	content := fs.String("content", "", "new body text")
	title := fs.String("title", "", "new title")
	typ := fs.String("type", "", "summary | important | note")
	importance := fs.Int("importance", 0, "importance from 1 to 5")
	var tags stringList
	fs.Var(&tags, "tag", "replace the whole tag list")
	var addTags stringList
	fs.Var(&addTags, "add-tag", "add a tag, repeatable")
	var removeTags stringList
	fs.Var(&removeTags, "remove-tag", "remove a tag, repeatable")
	scope := fs.String("scope", "", "new scope")
	source := fs.String("source", "", "new source")

	positional, err := parseFlagSet(fs, args, stdout)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	if len(positional) < 1 {
		return fail(g, stderr, newError(codeInvalidArgs, "update requires an id"))
	}
	id, err := parseID(positional[0])
	if err != nil {
		return fail(g, stderr, err)
	}

	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })

	in := store.UpdateInput{AddTags: addTags, RemoveTags: removeTags}
	if given["category"] {
		in.Category = category
	}
	if given["content"] {
		in.Content = content
	}
	if given["title"] {
		in.Title = title
	}
	if given["type"] {
		if *typ != store.TypeSummary && *typ != store.TypeImportant && *typ != store.TypeNote {
			return fail(g, stderr, newError(codeInvalidArgs, "type must be summary, important or note"))
		}
		in.Type = typ
	}
	if given["importance"] {
		if *importance < 1 || *importance > 5 {
			return fail(g, stderr, newError(codeInvalidArgs, "importance must be between 1 and 5"))
		}
		in.Importance = importance
	}
	if given["scope"] {
		in.Scope = scope
	}
	if given["source"] {
		in.Source = source
	}
	if given["tag"] {
		set := append([]string{}, tags...)
		in.SetTags = &set
	}

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	m, err := s.Update(id, in)
	if err != nil {
		return fail(g, stderr, err)
	}
	emit(g, stdout, toMemoryView(*m))
	return exitOK
}

// runDelete removes a record.
func runDelete(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	positional, err := parseFlagSet(fs, args, stdout)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	if len(positional) < 1 {
		return fail(g, stderr, newError(codeInvalidArgs, "delete requires an id"))
	}
	id, err := parseID(positional[0])
	if err != nil {
		return fail(g, stderr, err)
	}

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	if err := s.Delete(id); err != nil {
		return fail(g, stderr, err)
	}
	emit(g, stdout, deleteOutput{Deleted: id})
	return exitOK
}

// runCategories lists every category with its memory count.
func runCategories(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("categories", flag.ContinueOnError)
	sort := fs.String("sort", "name", "name | count | created_at")

	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	switch *sort {
	case "name", "count", "created_at":
	default:
		return fail(g, stderr, newError(codeInvalidArgs, "sort must be name, count or created_at"))
	}

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	cats, err := s.ListCategories(*sort)
	if err != nil {
		return fail(g, stderr, err)
	}
	out := make([]categoryView, 0, len(cats))
	for _, c := range cats {
		out = append(out, categoryView{
			Name:        c.Name,
			MemoryCount: c.MemoryCount,
			CreatedAt:   c.CreatedAt,
			UpdatedAt:   c.UpdatedAt,
		})
	}
	emit(g, stdout, out)
	return exitOK
}

// runStats summarises the store.
func runStats(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}

	s, err := openStore(g)
	if err != nil {
		return fail(g, stderr, err)
	}
	defer s.Close()

	st, err := s.Stats()
	if err != nil {
		return fail(g, stderr, err)
	}
	emit(g, stdout, st)
	return exitOK
}

// usageFlag documents one option of a command.
type usageFlag struct {
	Name     string `json:"name"`
	Required bool   `json:"required,omitempty"`
	Default  string `json:"default,omitempty"`
	Help     string `json:"help"`
}

// usageCommand documents one command.
type usageCommand struct {
	Name    string      `json:"name"`
	Summary string      `json:"summary"`
	Args    []string    `json:"args,omitempty"`
	Flags   []usageFlag `json:"flags,omitempty"`
}

// usageGuide is the machine readable manual handed to the AI.
type usageGuide struct {
	Program  string         `json:"program"`
	Purpose  string         `json:"purpose"`
	Defaults map[string]any `json:"defaults"`
	Commands []usageCommand `json:"commands"`
	Examples []string       `json:"examples"`
}

// usageGuideData builds the manual.
func usageGuideData() usageGuide {
	return usageGuide{
		Program: "aimemo",
		Purpose: "A memory store for AI agents. The agent picks its own categories and records summaries or important information.",
		Defaults: map[string]any{
			"db":       "environment AIMEMO_DB, otherwise ~/.aimemo/aimemo.db",
			"format":   "json",
			"exit_code": "0 success, 1 runtime error, 2 usage error",
		},
		Commands: []usageCommand{
			{
				Name:    "add",
				Summary: "Store a new memory. The category is created automatically when it does not exist.",
				Flags: []usageFlag{
					{Name: "category", Required: true, Help: "category name"},
					{Name: "content", Required: true, Help: "body text, - reads from stdin"},
					{Name: "title", Default: "", Help: "optional title"},
					{Name: "type", Default: "note", Help: "summary | important | note"},
					{Name: "importance", Default: "3", Help: "1 to 5"},
					{Name: "tag", Help: "repeatable"},
					{Name: "scope", Default: "", Help: "optional scope, for example a project name"},
					{Name: "source", Default: "", Help: "optional source"},
				},
			},
			{
				Name:    "list",
				Summary: "List memories by structure. Returns content_preview, not the full body.",
				Flags: []usageFlag{
					{Name: "category", Help: "repeatable, OR"},
					{Name: "scope", Help: "repeatable, OR"},
					{Name: "type", Help: "repeatable, OR"},
					{Name: "tag", Help: "repeatable, OR"},
					{Name: "min-importance", Help: "1 to 5"},
					{Name: "since", Help: "RFC3339 or YYYY-MM-DD"},
					{Name: "until", Help: "RFC3339 or YYYY-MM-DD"},
					{Name: "limit", Default: "20", Help: "1 to 500"},
					{Name: "offset", Default: "0", Help: "result offset"},
					{Name: "sort", Default: "created_at", Help: "created_at | importance"},
				},
			},
			{
				Name:    "search",
				Summary: "Full text search. Chinese substrings match exactly. Returns the full body and a match_context window.",
				Args:    []string{"QUERY"},
				Flags: []usageFlag{
					{Name: "category", Help: "repeatable, OR"},
					{Name: "scope", Help: "repeatable, OR"},
					{Name: "type", Help: "repeatable, OR"},
					{Name: "tag", Help: "repeatable, OR"},
					{Name: "min-importance", Help: "1 to 5"},
					{Name: "since", Help: "RFC3339 or YYYY-MM-DD"},
					{Name: "until", Help: "RFC3339 or YYYY-MM-DD"},
					{Name: "limit", Default: "20", Help: "1 to 500"},
					{Name: "offset", Default: "0", Help: "result offset"},
					{Name: "sort", Default: "importance", Help: "importance | created_at | relevance"},
					{Name: "loose", Default: "false", Help: "relax CJK phrase matching"},
				},
			},
			{
				Name:    "get",
				Summary: "Read one record by id, including the full body.",
				Args:    []string{"ID"},
			},
			{
				Name:    "update",
				Summary: "Change a record. Only the flags that are given are applied.",
				Args:    []string{"ID"},
				Flags: []usageFlag{
					{Name: "category", Help: "new category"},
					{Name: "content", Help: "new body"},
					{Name: "title", Help: "new title"},
					{Name: "type", Help: "summary | important | note"},
					{Name: "importance", Help: "1 to 5"},
					{Name: "scope", Help: "new scope"},
					{Name: "source", Help: "new source"},
					{Name: "tag", Help: "replace the whole tag list"},
					{Name: "add-tag", Help: "repeatable"},
					{Name: "remove-tag", Help: "repeatable"},
				},
			},
			{
				Name:    "delete",
				Summary: "Delete a record.",
				Args:    []string{"ID"},
			},
			{
				Name:    "categories",
				Summary: "List existing categories with their memory counts. Reuse an existing category instead of creating a near duplicate.",
				Flags: []usageFlag{
					{Name: "sort", Default: "name", Help: "name | count | created_at"},
				},
			},
			{
				Name:    "stats",
				Summary: "Counts by category, type and scope, plus the average importance.",
			},
			{
				Name:    "usage",
				Summary: "Print this guide.",
			},
		},
		Examples: []string{
			`aimemo add --category "技术选型" --type important --importance 5 --title "选型结论" --content "最终选择 SQLite 作为本地持久化数据库" --tag db --tag 选型`,
			`aimemo categories`,
			`aimemo search 数据库 --limit 5`,
			`aimemo list --category "技术选型" --min-importance 4 --sort importance`,
			`aimemo get 12`,
			`aimemo update 12 --importance 3 --add-tag 复核`,
			`aimemo delete 12`,
		},
	}
}

// runUsage prints the AI facing manual.
func runUsage(g globalOpts, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	if _, err := parseFlagSet(fs, args, stdout); err != nil {
		if errors.Is(err, errHelpRequested) {
			return exitOK
		}
		return fail(g, stderr, newError(codeInvalidArgs, "%v", err))
	}
	emit(g, stdout, usageGuideData())
	return exitOK
}
```

- [ ] **Step 3: 追加命令级测试到 internal/cli/root_test.go**

这两个测试需要真正的命令，所以放在 Task 8 而不是 Task 7：Task 7 里命令只是桩。

先在 `internal/cli/root_test.go` 的 import 块中加入 `"os"`，然后追加：

```go
func TestGlobalFlagsAreExtractedAnywhere(t *testing.T) {
	db := newDB(t)
	stdout, stderr, code := run(t, "--db", db, "add", "--category", "c",
		"--content", "hello", "--format", "json")
	if code != exitOK {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	m := decode(t, stdout)
	if m["ok"] != true {
		t.Errorf("envelope = %v", m)
	}
	if _, err := os.Stat(db); err != nil {
		t.Errorf("database was not created at %s: %v", db, err)
	}
}

func TestErrorEnvelopeAndExitCode(t *testing.T) {
	db := newDB(t)
	_, stderr, code := run(t, "get", "999", "--db", db)
	if code != exitError {
		t.Errorf("code = %d, want %d", code, exitError)
	}
	m := decode(t, stderr)
	if m["ok"] != false {
		t.Errorf("envelope = %v", m)
	}
	errObj, _ := m["error"].(map[string]any)
	if errObj["code"] != codeNotFound {
		t.Errorf("error code = %v, want %v", errObj["code"], codeNotFound)
	}
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd D:/work2026/aimemo
go build ./... && go test ./...
```

Expected: 编译通过，全部测试通过

- [ ] **Step 5: 提交**

```bash
cd D:/work2026/aimemo
git add internal/cli/ internal/store/
git commit -m "feat: implement all nine cli commands"
```

---

### Task 9: 端到端测试、构建与 README

**Files:**
- Create: `tests/e2e_test.go`
- Create: `README.md`
- Modify: `doc.go`

- [ ] **Step 1: 创建 tests/e2e_test.go**

```go
package tests

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"aimemo/internal/cli"
)

// run invokes the CLI against a temporary database and returns stdout, stderr
// and the exit code.
func run(t *testing.T, db string, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := cli.Run(append([]string{"--db", db}, args...), &out, &errb)
	return out.String(), errb.String(), code
}

// data decodes the success envelope and returns its data field.
func data(t *testing.T, stdout string) map[string]any {
	t.Helper()
	var env struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	if !env.OK {
		t.Fatalf("envelope reported failure: %s", stdout)
	}
	return env.Data
}

// errCode decodes the failure envelope and returns its error code.
func errCode(t *testing.T, stderr string) string {
	t.Helper()
	var env struct {
		OK    bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stderr), &env); err != nil {
		t.Fatalf("decode %q: %v", stderr, err)
	}
	return env.Error.Code
}

func newDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "aimemo.db")
}

func TestAddThenGet(t *testing.T) {
	db := newDB(t)
	stdout, stderr, code := run(t, db, "add", "--category", "技术选型",
		"--type", "important", "--importance", "5", "--title", "选型结论",
		"--content", "最终选择 SQLite 作为本地持久化数据库", "--tag", "db", "--tag", "选型")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	d := data(t, stdout)
	if d["category"] != "技术选型" {
		t.Errorf("category = %v", d["category"])
	}
	if d["created_at"] == "" || d["updated_at"] == "" {
		t.Errorf("expected timestamps: %v", d)
	}
	id := int(d["id"].(float64))

	stdout, stderr, code = run(t, db, "get", strconv.Itoa(id))
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	d = data(t, stdout)
	if d["content"] != "最终选择 SQLite 作为本地持久化数据库" {
		t.Errorf("content = %v", d["content"])
	}
}

func TestCategoryIsAutoCreatedAndReused(t *testing.T) {
	db := newDB(t)
	run(t, db, "add", "--category", "技术选型", "--content", "a")
	run(t, db, "add", "--category", "技术选型", "--content", "b")

	stdout, stderr, code := run(t, db, "categories")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	var env struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	if len(env.Data) != 1 {
		t.Fatalf("expected 1 category, got %d: %s", len(env.Data), stdout)
	}
	if env.Data[0]["memory_count"].(float64) != 2 {
		t.Errorf("memory_count = %v", env.Data[0]["memory_count"])
	}
}

func TestSearchChineseSubstring(t *testing.T) {
	db := newDB(t)
	run(t, db, "add", "--category", "技术选型", "--content", "最终选择 SQLite 作为本地持久化数据库")

	stdout, stderr, code := run(t, db, "search", "数据库")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	d := data(t, stdout)
	if d["total"].(float64) != 1 {
		t.Fatalf("total = %v, want 1: %s", d["total"], stdout)
	}
	items := d["items"].([]any)
	item := items[0].(map[string]any)
	if item["match_context"] == "" {
		t.Errorf("expected match_context: %v", item)
	}
	if strings.Contains(item["match_context"].(string), "数 据 库") {
		t.Errorf("match_context contains padded text: %v", item["match_context"])
	}
}

func TestSearchMixedCJKAndLatin(t *testing.T) {
	db := newDB(t)
	run(t, db, "add", "--category", "技术选型", "--content", "使用 Redis 作为缓存方案")

	stdout, stderr, code := run(t, db, "search", "redis")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	d := data(t, stdout)
	if d["total"].(float64) != 1 {
		t.Fatalf("total = %v, want 1: %s", d["total"], stdout)
	}
}

func TestSearchWithFilterAfterPositional(t *testing.T) {
	db := newDB(t)
	run(t, db, "add", "--category", "技术选型", "--content", "数据库选型结论")
	run(t, db, "add", "--category", "会议纪要", "--content", "数据库讨论")

	stdout, stderr, code := run(t, db, "search", "数据库", "--category", "会议纪要")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	d := data(t, stdout)
	if d["total"].(float64) != 1 {
		t.Fatalf("total = %v, want 1: %s", d["total"], stdout)
	}
}

func TestListReturnsPreviewNotFullContent(t *testing.T) {
	db := newDB(t)
	long := strings.Repeat("甲", 500)
	run(t, db, "add", "--category", "c", "--content", long)

	stdout, stderr, code := run(t, db, "list")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	d := data(t, stdout)
	item := d["items"].([]any)[0].(map[string]any)
	if _, ok := item["content"]; ok {
		t.Errorf("list should not return the full body: %v", item)
	}
	preview := item["content_preview"].(string)
	if len([]rune(preview)) != 201 {
		t.Errorf("preview length = %d, want 201 (200 plus ellipsis)", len([]rune(preview)))
	}
}

func TestUpdateOnlyChangesGivenFields(t *testing.T) {
	db := newDB(t)
	stdout, _, _ := run(t, db, "add", "--category", "c", "--content", "旧内容",
		"--title", "旧标题", "--importance", "2")
	id := int(data(t, stdout)["id"].(float64))

	stdout, stderr, code := run(t, db, "update", strconv.Itoa(id), "--importance", "5")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	d := data(t, stdout)
	if d["importance"].(float64) != 5 {
		t.Errorf("importance = %v", d["importance"])
	}
	if d["content"] != "旧内容" {
		t.Errorf("content should be untouched: %v", d["content"])
	}
	if d["title"] != "旧标题" {
		t.Errorf("title should be untouched: %v", d["title"])
	}
}

func TestUpdateAddAndRemoveTag(t *testing.T) {
	db := newDB(t)
	stdout, _, _ := run(t, db, "add", "--category", "c", "--content", "x", "--tag", "a", "--tag", "b")
	id := int(data(t, stdout)["id"].(float64))

	stdout, stderr, code := run(t, db, "update", strconv.Itoa(id), "--add-tag", "c", "--remove-tag", "a")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	tags := data(t, stdout)["tags"].([]any)
	if len(tags) != 2 {
		t.Fatalf("tags = %v", tags)
	}
	seen := map[string]bool{}
	for _, tg := range tags {
		seen[tg.(string)] = true
	}
	if seen["a"] || !seen["b"] || !seen["c"] {
		t.Errorf("tags = %v", tags)
	}
}

func TestDeleteThenGetIsNotFound(t *testing.T) {
	db := newDB(t)
	stdout, _, _ := run(t, db, "add", "--category", "c", "--content", "x")
	id := int(data(t, stdout)["id"].(float64))

	if _, stderr, code := run(t, db, "delete", strconv.Itoa(id)); code != 0 {
		t.Fatalf("delete failed: %d %s", code, stderr)
	}
	_, stderr, code := run(t, db, "get", strconv.Itoa(id))
	if code != 1 {
		t.Errorf("code = %d, want 1", code)
	}
	if got := errCode(t, stderr); got != "not_found" {
		t.Errorf("error code = %q, want not_found", got)
	}
}

func TestInvalidArgumentsProduceInvalidArgs(t *testing.T) {
	db := newDB(t)
	cases := [][]string{
		{"add", "--content", "x"},
		{"add", "--category", "c"},
		{"add", "--category", "c", "--content", "x", "--importance", "9"},
		{"add", "--category", "c", "--content", "x", "--type", "weird"},
		{"list", "--limit", "0"},
		{"list", "--min-importance", "9"},
		{"get", "abc"},
		{"search"},
	}
	for _, args := range cases {
		_, stderr, code := run(t, db, args...)
		if code != 2 {
			t.Errorf("%v: code = %d, want 2 (stderr %s)", args, code, stderr)
			continue
		}
		if got := errCode(t, stderr); got != "invalid_args" {
			t.Errorf("%v: error code = %q, want invalid_args", args, got)
		}
	}
}

func TestUsageListsEveryCommand(t *testing.T) {
	db := newDB(t)
	stdout, stderr, code := run(t, db, "usage")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	for _, cmd := range []string{"add", "list", "search", "get", "update", "delete", "categories", "stats", "usage"} {
		if !strings.Contains(stdout, `"`+cmd+`"`) {
			t.Errorf("usage does not mention %q", cmd)
		}
	}
}

func TestTextFormatOmitsEnvelope(t *testing.T) {
	db := newDB(t)
	stdout, _, code := run(t, db, "--format", "text", "add", "--category", "c", "--content", "hello")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if strings.Contains(stdout, `"ok"`) {
		t.Errorf("text format should not contain the JSON envelope: %s", stdout)
	}
	if !strings.Contains(stdout, "hello") {
		t.Errorf("text format should contain the content: %s", stdout)
	}
}

func TestDatabaseCreatedAtDefaultPathIsOverridable(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "nested", "aimemo.db")
	if _, stderr, code := run(t, db, "add", "--category", "c", "--content", "x"); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	if _, err := os.Stat(db); err != nil {
		t.Errorf("database was not created: %v", err)
	}
}
```

- [ ] **Step 2: 运行端到端测试**

```bash
cd D:/work2026/aimemo
go test ./tests/ 2>&1
```

Expected: PASS

若 `TestCategoryIsAutoCreatedAndReused` 因解码方式报错，把该测试中的解码改为：

```go
	var env struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	cats := env.Data
```

- [ ] **Step 3: 全量测试与静态检查**

```bash
cd D:/work2026/aimemo
go vet ./... && go test ./... -count=1
```

Expected: `go vet` 无输出；全部测试通过

- [ ] **Step 4: 构建可执行文件**

```bash
cd D:/work2026/aimemo
CGO_ENABLED=0 go build -o aimemo.exe .
```

Expected: 生成 `aimemo.exe`

- [ ] **Step 5: 手工冒烟测试**

```bash
cd D:/work2026/aimemo
./aimemo.exe --db ./smoke.db add --category "技术选型" --type important --importance 5 --content "最终选择 SQLite 作为本地持久化数据库" --tag db
./aimemo.exe --db ./smoke.db categories
./aimemo.exe --db ./smoke.db search 数据库
./aimemo.exe --db ./smoke.db list --sort importance
./aimemo.exe --db ./smoke.db stats
```

Expected: 每条命令输出 `{"ok": true, ...}`；`search 数据库` 命中刚写入的记录

```bash
rm -f ./smoke.db ./smoke.db-shm ./smoke.db-wal
```

- [ ] **Step 6: 创建 README.md**

````markdown
# aimemo

供 AI 调用的记忆存储程序。AI 自行决定分类，记录会话摘要或重要信息，数据存入 SQLite，所有数据表带创建时间。

## 构建

```bash
CGO_ENABLED=0 go build -o aimemo.exe .
```

## 调用

```bash
# 写入一条重要信息，分类不存在时自动创建
aimemo add --category "技术选型" --type important --importance 5 \
  --title "选型结论" --content "最终选择 SQLite 作为本地持久化数据库" --tag db

# 查看已有分类，复用而不是新建近义分类
aimemo categories

# 全文检索，中文按子串精确匹配
aimemo search 数据库

# 结构化列举
aimemo list --category "技术选型" --min-importance 4 --sort importance
```

## 输出契约

成功时 stdout 输出 `{"ok": true, "data": {...}}`；失败时 stderr 输出
`{"ok": false, "error": {"code": "...", "message": "..."}}`。

退出码：`0` 成功，`1` 运行时错误，`2` 用法错误。

错误码：`invalid_args`、`not_found`、`conflict`、`db_error`、`io_error`。

## 命令

| 命令 | 作用 |
|---|---|
| `add` | 写入记忆，分类不存在则自动创建 |
| `list` | 按分类/作用域/类型/标签/重要度/时间过滤列举 |
| `search` | 全文检索，返回正文与命中上下文 |
| `get` | 按 id 取单条完整记录 |
| `update` | 修改字段，仅覆盖显式传入的项 |
| `delete` | 删除记录 |
| `categories` | 列出已有分类及条目数 |
| `stats` | 分类/类型/作用域分布统计 |
| `usage` | 输出给 AI 看的使用指南 |

完整参数见 `aimemo usage`。

## 数据库

默认路径 `~/.aimemo/aimemo.db`，可用 `--db PATH` 或环境变量 `AIMEMO_DB` 覆盖。

表：`categories`、`memories`、`memory_tags`，以及 FTS5 索引 `memories_fts`。
每张表都有 `created_at`，`memories` 与 `categories` 另有 `updated_at`。
````

- [ ] **Step 7: 更新 doc.go**

用以下内容替换 `doc.go`：

```go
// aimemo is a memory store for AI agents.
//
// The agent picks its own categories and records summaries or important
// information. Data lives in SQLite and every table carries a creation time.
//
// See README.md and docs/superpowers/specs/2026-10-22-aimemo-design.md.
package main
```

- [ ] **Step 8: 提交**

```bash
cd D:/work2026/aimemo
git add tests/ README.md doc.go
git commit -m "test: add end-to-end cli tests and document usage"
```

---

## 验收标准

全部满足才算完成：

1. `go vet ./...` 无输出
2. `go test ./... -count=1` 全部通过
3. `CGO_ENABLED=0 go build -o aimemo.exe .` 成功
4. `aimemo.exe search 数据库` 能命中含"数据库"的记录
5. `aimemo.exe search redis` 能命中"使用 Redis 作为缓存方案"
6. `aimemo.exe categories` 的 `memory_count` 与实际写入条数一致
7. 所有响应均为 JSON 信封格式，错误码与退出码符合上表
8. `aimemo.exe usage` 列出全部九个命令

