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
	for _, cmd := range []string{
		"add", "list", "search", "context", "get", "update", "delete", "use", "feedback",
		"categories", "stats", "usage", "export-md", "import-md", "init", "doctor", "backup",
	} {
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

func TestInitWritesAgentsAndSkill(t *testing.T) {
	db := newDB(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := run(t, db, "init", "--dir", dir)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	d := data(t, stdout)
	if d["language"] != "go" {
		t.Errorf("language = %v, want go", d["language"])
	}
	for _, name := range []string{
		"AGENTS.md",
		filepath.Join(".opencode", "skills", "aimemo", "skill.yaml"),
		filepath.Join(".opencode", "skills", "aimemo", "skill.json"),
		filepath.Join(".opencode", "skills", "aimemo", "README.md"),
		filepath.Join(".opencode", "agent", "memory-retriever.md"),
		filepath.Join(".opencode", "agent", "memory-curator.md"),
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	agents, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agents), "`go`") {
		t.Errorf("AGENTS.md missing detected language: %s", agents)
	}
}

func TestInitMergesAgentsWithoutForce(t *testing.T) {
	db := newDB(t)
	dir := t.TempDir()
	if _, _, code := run(t, db, "init", "--dir", dir); code != 0 {
		t.Fatalf("first init failed")
	}
	custom := "# 团队约定\n\n请保留这段自定义内容。\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(custom), 0644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := run(t, db, "init", "--dir", dir); code != 0 {
		t.Fatalf("second init failed: %s", stderr)
	}
	body, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "请保留这段自定义内容") {
		t.Errorf("user content was lost: %s", body)
	}
	if !strings.Contains(string(body), "<!-- aimemo:begin -->") {
		t.Errorf("managed block missing: %s", body)
	}
	if strings.Count(string(body), "<!-- aimemo:begin -->") != 1 {
		t.Errorf("managed block was duplicated: %s", body)
	}
}

func TestExportMDWritesMarkdown(t *testing.T) {
	db := newDB(t)
	run(t, db, "add", "--category", "skills/go", "--type", "important", "--importance", "5", "--title", "Error Wrap", "--content", "wrap errors with context", "--tag", "lang:go")
	dir := t.TempDir()
	stdout, stderr, code := run(t, db, "export-md", "--out", dir)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	d := data(t, stdout)
	if d["exported"].(float64) != 1 {
		t.Errorf("exported = %v, want 1", d["exported"])
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "skills", "go", "*.md"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 markdown file, got %v", matches)
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "wrap errors with context") {
		t.Errorf("markdown missing content: %s", body)
	}
}

func TestContextRanksRelevantMemory(t *testing.T) {
	db := newDB(t)
	run(t, db, "add", "--category", "gotchas/go", "--type", "important", "--importance", "5",
		"--title", "sqlite busy timeout", "--content", "遇到 database is locked 时设置 busy_timeout 并开启 WAL",
		"--tag", "lang:go", "--tag", "sqlite")
	run(t, db, "add", "--category", "skills/ts", "--type", "note", "--importance", "5",
		"--title", "React hooks cleanup", "--content", "useEffect 返回清理函数以避免泄漏", "--tag", "lang:ts")

	stdout, stderr, code := run(t, db, "context", "sqlite busy 超时 database is locked", "--limit", "3")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	d := data(t, stdout)
	items, _ := d["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("context returned no items: %s", stdout)
	}
	first, _ := items[0].(map[string]any)
	if first["category"] != "gotchas/go" {
		t.Errorf("first item = %v, want the sqlite memory", first)
	}
	if d["context"] == "" {
		t.Error("context block is empty")
	}

	stdout, _, code = run(t, db, "--format", "prompt", "context", "sqlite busy timeout")
	if code != 0 {
		t.Fatalf("prompt format failed")
	}
	if strings.Contains(stdout, `"ok"`) {
		t.Errorf("prompt format leaked JSON: %s", stdout)
	}
	if !strings.Contains(stdout, "busy_timeout") {
		t.Errorf("prompt format missing memory body: %s", stdout)
	}

	stdout, _, code = run(t, db, "context", "sqlite busy timeout", "--explain")
	if code != 0 {
		t.Fatalf("explain failed")
	}
	if !strings.Contains(stdout, `"breakdown"`) {
		t.Errorf("explain did not include a breakdown: %s", stdout)
	}
}

func TestUseAndFeedbackCounters(t *testing.T) {
	db := newDB(t)
	stdout, _, code := run(t, db, "add", "--category", "c", "--content", "x")
	if code != 0 {
		t.Fatalf("add failed")
	}
	id := strconv.Itoa(int(data(t, stdout)["id"].(float64)))

	for _, args := range [][]string{
		{"use", id},
		{"feedback", id, "--helpful"},
		{"feedback", id, "--wrong"},
	} {
		if _, stderr, code := run(t, db, args...); code != 0 {
			t.Fatalf("%v failed: %s", args, stderr)
		}
	}
	stdout, _, code = run(t, db, "get", id)
	if code != 0 {
		t.Fatalf("get failed")
	}
	d := data(t, stdout)
	if d["use_count"].(float64) != 1 || d["helpful_count"].(float64) != 1 || d["wrong_count"].(float64) != 1 {
		t.Errorf("counters = %v", d)
	}
	if d["last_used_at"] == "" {
		t.Error("last_used_at was not set")
	}

	_, stderr, code := run(t, db, "feedback", id)
	if code == 0 || errCode(t, stderr) != "invalid_args" {
		t.Errorf("feedback without a verdict should fail: %s", stderr)
	}
}

func TestAddRejectsDuplicateTitleAndSupersedes(t *testing.T) {
	db := newDB(t)
	stdout, _, _ := run(t, db, "add", "--category", "c", "--title", "same", "--content", "v1")
	first := int64(data(t, stdout)["id"].(float64))

	_, stderr, code := run(t, db, "add", "--category", "c", "--title", "same", "--content", "v2")
	if code == 0 || errCode(t, stderr) != "conflict" {
		t.Fatalf("duplicate title should conflict: %s", stderr)
	}
	if _, _, code := run(t, db, "add", "--category", "c", "--title", "same", "--content", "v2", "--allow-duplicate"); code != 0 {
		t.Fatal("--allow-duplicate should bypass the check")
	}

	stdout, stderr, code = run(t, db, "add", "--category", "c", "--title", "same v3", "--content", "v3", "--supersedes", strconv.FormatInt(first, 10))
	if code != 0 {
		t.Fatalf("supersede add failed: %s", stderr)
	}
	if data(t, stdout)["supersedes"].(float64) != float64(first) {
		t.Errorf("supersedes = %v", data(t, stdout)["supersedes"])
	}
	stdout, _, _ = run(t, db, "list")
	if strings.Contains(stdout, "v1") {
		t.Errorf("superseded memory should be hidden by default: %s", stdout)
	}
	stdout, _, _ = run(t, db, "list", "--include-expired")
	if !strings.Contains(stdout, "v1") {
		t.Errorf("--include-expired should keep the superseded memory: %s", stdout)
	}
}

func TestWorkspaceIsolation(t *testing.T) {
	db := newDB(t)
	if _, stderr, code := run(t, db, "--workspace", "alpha", "add", "--category", "c", "--title", "a", "--content", "alpha note"); code != 0 {
		t.Fatalf("add failed: %s", stderr)
	}
	if _, _, code := run(t, db, "add", "--category", "c", "--title", "b", "--content", "global note"); code != 0 {
		t.Fatal("add failed")
	}

	stdout, _, _ := run(t, db, "list")
	if strings.Contains(stdout, "alpha note") || !strings.Contains(stdout, "global note") {
		t.Errorf("default list should only show the default workspace: %s", stdout)
	}
	stdout, _, _ = run(t, db, "--workspace", "alpha", "list")
	if !strings.Contains(stdout, "alpha note") || strings.Contains(stdout, "global note") {
		t.Errorf("workspace list leaked records: %s", stdout)
	}
}

func TestValidUntilFiltersExpired(t *testing.T) {
	db := newDB(t)
	run(t, db, "add", "--category", "c", "--title", "old", "--content", "outdated", "--valid-until", "2020-01-01")
	stdout, _, _ := run(t, db, "list")
	if strings.Contains(stdout, "outdated") {
		t.Errorf("expired memory should be hidden by default: %s", stdout)
	}
	stdout, _, _ = run(t, db, "list", "--include-expired")
	if !strings.Contains(stdout, "outdated") {
		t.Errorf("--include-expired should keep it: %s", stdout)
	}
}

func TestDoctorAndBackup(t *testing.T) {
	db := newDB(t)
	run(t, db, "add", "--category", "c", "--content", "x")

	stdout, stderr, code := run(t, db, "doctor")
	if code != 0 {
		t.Fatalf("doctor failed: %s", stderr)
	}
	if data(t, stdout)["healthy"] != true {
		t.Errorf("doctor reported unhealthy: %s", stdout)
	}

	backup := filepath.Join(t.TempDir(), "copy.db")
	if _, stderr, code := run(t, db, "backup", "--out", backup); code != 0 {
		t.Fatalf("backup failed: %s", stderr)
	}
	stdout, stderr, code = run(t, backup, "list")
	if code != 0 {
		t.Fatalf("backup is not readable: %s", stderr)
	}
	if !strings.Contains(stdout, `"total": 1`) && !strings.Contains(stdout, `"total":1`) {
		t.Errorf("backup lost rows: %s", stdout)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	db := newDB(t)
	run(t, db, "add", "--category", "skills/go", "--type", "important", "--importance", "5",
		"--title", "错误包装", "--content", "用 fmt.Errorf 的 %w 保留错误链",
		"--problem", "错误信息丢失", "--solution", "使用 %w", "--pitfall", "不要用 %v", "--tag", "lang:go")
	dir := t.TempDir()
	if _, stderr, code := run(t, db, "export-md", "--out", dir); code != 0 {
		t.Fatalf("export failed: %s", stderr)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "skills", "go", "*.md"))
	if len(matches) != 1 {
		t.Fatalf("expected one file, got %v", matches)
	}
	if !strings.Contains(filepath.Base(matches[0]), "错误包装") {
		t.Errorf("slug dropped CJK characters: %s", matches[0])
	}
	body, _ := os.ReadFile(matches[0])
	if !strings.Contains(string(body), "## 解决方案") {
		t.Errorf("missing structured section: %s", body)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
		t.Errorf("missing index page: %v", err)
	}

	fresh := newDB(t)
	stdout, stderr, code := run(t, fresh, "import-md", "--dir", dir)
	if code != 0 {
		t.Fatalf("import failed: %s", stderr)
	}
	if data(t, stdout)["created"].(float64) != 1 {
		t.Errorf("import created = %v", data(t, stdout)["created"])
	}
	stdout, _, _ = run(t, fresh, "search", "错误链")
	if !strings.Contains(stdout, "错误包装") {
		t.Errorf("imported memory missing: %s", stdout)
	}
	found := data(t, stdout)
	items, _ := found["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("imported memory not searchable: %s", stdout)
	}
	item, _ := items[0].(map[string]any)
	if item["content"] != "用 fmt.Errorf 的 %w 保留错误链" {
		t.Errorf("imported content = %v", item["content"])
	}

	stdout, _, code = run(t, fresh, "import-md", "--dir", dir, "--overwrite")
	if code != 0 {
		t.Fatalf("overwrite import failed")
	}
	if data(t, stdout)["updated"].(float64) != 1 {
		t.Errorf("overwrite import did not update: %s", stdout)
	}
}

func TestExportRejectsTraversalCategory(t *testing.T) {
	db := newDB(t)
	if _, stderr, code := run(t, db, "add", "--category", "../evil", "--content", "escape"); code != 0 {
		t.Fatalf("add failed: %s", stderr)
	}
	dir := t.TempDir()
	_, stderr, code := run(t, db, "export-md", "--out", filepath.Join(dir, "out"))
	if code == 0 {
		t.Fatal("traversal category should be rejected")
	}
	if errCode(t, stderr) != "invalid_args" {
		t.Errorf("error code = %q", errCode(t, stderr))
	}
	if _, err := os.Stat(filepath.Join(dir, "evil")); !os.IsNotExist(err) {
		t.Errorf("export escaped the output directory")
	}
}

func TestContextRecallAt3(t *testing.T) {
	db := newDB(t)
	cases := []struct {
		title   string
		content string
		tags    []string
		query   string
	}{
		{"sqlite busy timeout", "database is locked 时设置 busy_timeout 并开启 WAL", []string{"sqlite"}, "sqlite 数据库锁超时"},
		{"react cleanup", "useEffect 返回清理函数避免内存泄漏", []string{"react"}, "react 内存泄漏"},
		{"go error wrap", "用 fmt.Errorf 的 %w 包装错误保留调用链", []string{"go"}, "golang 错误处理 包装"},
		{"docker layer cache", "把依赖安装放在 COPY 之前以复用 docker 层缓存", []string{"docker"}, "docker 构建缓存慢"},
		{"postgres index", "为高频过滤列建立 btree 索引并分析执行计划", []string{"postgres"}, "postgres 查询慢 索引"},
	}
	ids := make([]int64, len(cases))
	for i, c := range cases {
		args := []string{"add", "--category", "eval", "--importance", "4", "--title", c.title, "--content", c.content}
		for _, tag := range c.tags {
			args = append(args, "--tag", tag)
		}
		stdout, stderr, code := run(t, db, args...)
		if code != 0 {
			t.Fatalf("add %s failed: %s", c.title, stderr)
		}
		ids[i] = int64(data(t, stdout)["id"].(float64))
	}

	hits := 0
	for i, c := range cases {
		stdout, _, code := run(t, db, "context", c.query, "--limit", "3")
		if code != 0 {
			t.Fatalf("context %q failed", c.query)
		}
		items, _ := data(t, stdout)["items"].([]any)
		found := false
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if int64(item["id"].(float64)) == ids[i] {
				found = true
				break
			}
		}
		if found {
			hits++
		} else {
			t.Errorf("recall@3 missed %q while searching %q", c.title, c.query)
		}
	}
	if recall := float64(hits) / float64(len(cases)); recall < 0.8 {
		t.Errorf("recall@3 = %.2f, want >= 0.80", recall)
	}
}
