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
	cats, err := s.ListCategories("name")
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
