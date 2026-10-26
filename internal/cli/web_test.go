package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aimemo/internal/store"
)

func newWebTest(t *testing.T) (*store.Store, *webServer) {
	t.Helper()
	s, err := store.Open(newDB(t))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, &webServer{store: s, workspace: "alpha"}
}

func webDo(ws *webServer, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, nil)
	securityHeaders(ws.routes()).ServeHTTP(rec, req)
	return rec
}

func webJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	return out
}

func webItems(t *testing.T, out map[string]any) []any {
	t.Helper()
	data, _ := out["data"].(map[string]any)
	items, _ := data["items"].([]any)
	return items
}

func TestWebIndexHealthAndHeaders(t *testing.T) {
	_, ws := newWebTest(t)

	rec := webDo(ws, http.MethodGet, "/")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "aimemo") {
		t.Fatalf("index = %d %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Security-Policy"); got == "" {
		t.Error("missing CSP header")
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("nosniff = %q", got)
	}

	rec = webDo(ws, http.MethodGet, "/healthz")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}

func TestWebListIsolatesWorkspaces(t *testing.T) {
	s, ws := newWebTest(t)
	if _, err := s.Create(store.CreateInput{Category: "c", Title: "alpha memory", Content: "alpha body", Workspace: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(store.CreateInput{Category: "c", Title: "beta memory", Content: "beta body", Workspace: "beta"}); err != nil {
		t.Fatal(err)
	}

	items := webItems(t, webJSON(t, webDo(ws, http.MethodGet, "/api/list")))
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	first := items[0].(map[string]any)
	if first["title"] != "alpha memory" || first["workspace"] != "alpha" {
		t.Fatalf("unexpected item: %v", first)
	}
}

func TestWebGetRejectsOtherWorkspace(t *testing.T) {
	s, ws := newWebTest(t)
	alpha, err := s.Create(store.CreateInput{Category: "c", Title: "alpha", Content: "body", Workspace: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	beta, err := s.Create(store.CreateInput{Category: "c", Title: "beta", Content: "body", Workspace: "beta"})
	if err != nil {
		t.Fatal(err)
	}

	rec := webDo(ws, http.MethodGet, "/api/get?id="+itoa(alpha.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("alpha status = %d", rec.Code)
	}
	rec = webDo(ws, http.MethodGet, "/api/get?id="+itoa(beta.ID))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("beta status = %d, want 404", rec.Code)
	}
}

func TestWebSearchUsesFTS(t *testing.T) {
	s, ws := newWebTest(t)
	if _, err := s.Create(store.CreateInput{Category: "c", Title: "sqlite concurrency", Content: "use busy timeout", Workspace: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(store.CreateInput{Category: "c", Title: "unrelated", Content: "nothing here", Workspace: "alpha"}); err != nil {
		t.Fatal(err)
	}

	rec := webDo(ws, http.MethodGet, "/api/list?q=sqlite")
	items := webItems(t, webJSON(t, rec))
	if len(items) != 1 {
		t.Fatalf("search items = %d, want 1 body=%s", len(items), rec.Body.String())
	}
	if items[0].(map[string]any)["title"] != "sqlite concurrency" {
		t.Fatalf("unexpected search result: %v", items[0])
	}
}

func TestWebPagination(t *testing.T) {
	s, ws := newWebTest(t)
	for i := 0; i < 3; i++ {
		if _, err := s.Create(store.CreateInput{Category: "c", Title: "m" + itoa(int64(i)), Content: "body", Workspace: "alpha"}); err != nil {
			t.Fatal(err)
		}
	}

	out := webJSON(t, webDo(ws, http.MethodGet, "/api/list?limit=2&offset=0"))
	data := out["data"].(map[string]any)
	if data["total"].(float64) != 3 {
		t.Fatalf("total = %v", data["total"])
	}
	if len(webItems(t, out)) != 2 {
		t.Fatalf("first page size = %d", len(webItems(t, out)))
	}
	out = webJSON(t, webDo(ws, http.MethodGet, "/api/list?limit=2&offset=2"))
	if len(webItems(t, out)) != 1 {
		t.Fatalf("second page size = %d", len(webItems(t, out)))
	}
}

func TestWebStatsAreScoped(t *testing.T) {
	s, ws := newWebTest(t)
	if _, err := s.Create(store.CreateInput{Category: "alpha-cat", Title: "a", Content: "b", Workspace: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(store.CreateInput{Category: "beta-cat", Title: "b", Content: "b", Workspace: "beta"}); err != nil {
		t.Fatal(err)
	}

	out := webJSON(t, webDo(ws, http.MethodGet, "/api/stats"))
	data := out["data"].(map[string]any)
	stats := data["stats"].(map[string]any)
	if stats["total"].(float64) != 1 {
		t.Fatalf("total = %v", stats["total"])
	}
	cats, _ := data["categories"].([]any)
	if len(cats) != 1 || cats[0].(map[string]any)["name"] != "alpha-cat" {
		t.Fatalf("categories = %v", cats)
	}
}

func TestWebStaticNotFound(t *testing.T) {
	_, ws := newWebTest(t)
	if rec := webDo(ws, http.MethodGet, "/static/missing.js"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec := webDo(ws, http.MethodGet, "/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
