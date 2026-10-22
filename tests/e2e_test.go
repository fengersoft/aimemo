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
