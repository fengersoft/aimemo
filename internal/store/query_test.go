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
