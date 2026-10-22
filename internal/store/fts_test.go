package store

import (
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
	if strings.Contains(got, `a"b`) {
		t.Errorf("BuildMatch did not escape quote: %q", got)
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
