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

func TestCreateReusesCategoryIgnoringCase(t *testing.T) {
	s := newTestStore(t)
	a, _ := s.Create(CreateInput{Category: "技术选型", Content: "x"})
	b, _ := s.Create(CreateInput{Category: "技术选型", Content: "y"})
	if a.CategoryID != b.CategoryID {
		t.Errorf("expected same category, got %d and %d", a.CategoryID, b.CategoryID)
	}
	cats, err := s.ListCategories("name", "")
	if err != nil {
		t.Fatalf("ListCategories: %v", err)
	}
	if len(cats) != 1 {
		t.Errorf("expected 1 category, got %d", len(cats))
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
	got, err := s.Get(created.ID, "")
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
	if _, err := s.Get(999, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateChangesOnlyGivenFields(t *testing.T) {
	s := newTestStore(t)
	m, _ := s.Create(CreateInput{
		Category: "c", Content: "旧内容", Title: "旧标题",
		Type: "note", Importance: 2, Scope: "old", Source: "old-src",
		Tags: []string{"a", "b"},
	})
	newTitle := "新标题"
	got, err := s.Update(m.ID, "", UpdateInput{Title: &newTitle})
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
	got, err := s.Update(m.ID, "", UpdateInput{AddTags: []string{"c"}, RemoveTags: []string{"a"}})
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
	if _, err := s.Update(m.ID, "", UpdateInput{Content: &newContent}); err != nil {
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
	got, err := s.Update(m.ID, "", UpdateInput{SetTags: &replacement})
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
	if _, err := s.Update(m.ID, "", UpdateInput{SetTags: &replacement}); !errors.Is(err, ErrInvalid) {
		t.Errorf("expected ErrInvalid for duplicate tags, got %v", err)
	}
}

func TestUpdateNotFound(t *testing.T) {
	s := newTestStore(t)
	title := "x"
	if _, err := s.Update(999, "", UpdateInput{Title: &title}); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	m, _ := s.Create(CreateInput{Category: "c", Content: "x", Tags: []string{"t"}})
	if err := s.Delete(m.ID, ""); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(m.ID, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
	if err := s.Delete(m.ID, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound on second delete, got %v", err)
	}
}
