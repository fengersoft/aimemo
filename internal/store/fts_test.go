package store

import (
	"database/sql"
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

func TestBuildMatchMatchesIndexedText(t *testing.T) {
	s := newTestStore(t)
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
	s := newTestStore(t)
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
	s := newTestStore(t)
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
	s := newTestStore(t)
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
