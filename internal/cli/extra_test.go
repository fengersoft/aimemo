package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	SetVersion("v9.9.9", "c0ffee", "2026-10-23")
	out, _, code := run(t, "version")
	if code != exitOK {
		t.Fatalf("code = %d", code)
	}
	data := decode(t, out)["data"].(map[string]any)
	if data["version"] != "v9.9.9" || data["commit"] != "c0ffee" {
		t.Fatalf("unexpected version output: %v", data)
	}
}

func TestConfigPrecedence(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg := `{"db": "` + filepath.ToSlash(filepath.Join(dir, "cfg.db")) + `", "workspace": "cfgws"}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIMEMO_CONFIG", cfgPath)
	t.Setenv("AIMEMO_DB", "")
	t.Setenv("AIMEMO_WORKSPACE", "")

	g, _ := extractGlobals(nil)
	if g.workspace != "cfgws" {
		t.Errorf("workspace = %q, want cfgws", g.workspace)
	}
	if filepath.ToSlash(g.dbPath) != filepath.ToSlash(filepath.Join(dir, "cfg.db")) {
		t.Errorf("db = %q", g.dbPath)
	}

	g, _ = extractGlobals([]string{"--workspace", "flagws", "--db", "flag.db"})
	if g.workspace != "flagws" || g.dbPath != "flag.db" {
		t.Errorf("flags did not win: %+v", g)
	}
}

func TestPruneExpired(t *testing.T) {
	db := newDB(t)
	run(t, "add", "--db", db, "--category", "c", "--title", "old", "--content", "x", "--valid-until", "2020-01-01")
	run(t, "add", "--db", db, "--category", "c", "--title", "fresh", "--content", "y")

	out, _, code := run(t, "prune", "--db", db, "--expired", "--dry-run")
	if code != exitOK {
		t.Fatalf("dry-run code = %d", code)
	}
	data := out
	m := decode(t, data)["data"].(map[string]any)
	if m["deleted"].(float64) != 0 || len(m["ids"].([]any)) != 1 {
		t.Fatalf("dry-run = %v", m)
	}

	out, _, code = run(t, "prune", "--db", db, "--expired", "--force")
	if code != exitOK {
		t.Fatalf("prune code = %d", code)
	}
	if decode(t, out)["data"].(map[string]any)["deleted"].(float64) != 1 {
		t.Fatalf("prune result = %s", out)
	}

	out, _, _ = run(t, "list", "--db", db)
	if decode(t, out)["data"].(map[string]any)["total"].(float64) != 1 {
		t.Fatalf("remaining = %s", out)
	}
}

func TestExportImportJSONRoundTrip(t *testing.T) {
	src := newDB(t)
	dst := newDB(t)
	run(t, "add", "--db", src, "--workspace", "w", "--category", "c", "--title", "one", "--content", "body one", "--tag", "go")
	run(t, "add", "--db", src, "--workspace", "w", "--category", "c", "--title", "two", "--content", "body two")

	path := filepath.Join(t.TempDir(), "export.json")
	if _, _, code := run(t, "export-json", "--db", src, "--all", "--out", path); code != exitOK {
		t.Fatalf("export code = %d", code)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err != nil || len(list) != 2 {
		t.Fatalf("export = %d items err=%v", len(list), err)
	}

	out, _, code := run(t, "import-json", "--db", dst, "--in", path)
	if code != exitOK {
		t.Fatalf("import code = %d out=%s", code, out)
	}
	data := decode(t, out)["data"].(map[string]any)
	if data["created"].(float64) != 2 {
		t.Fatalf("import = %s", out)
	}
	out, _, _ = run(t, "list", "--db", dst, "--workspace", "w")
	if decode(t, out)["data"].(map[string]any)["total"].(float64) != 2 {
		t.Fatalf("dst list = %s", out)
	}

	if _, _, code := run(t, "import-json", "--db", dst, "--in", path); code != exitOK {
		t.Fatalf("second import code = %d", code)
	}
	out, _, _ = run(t, "import-json", "--db", dst, "--in", path, "--overwrite")
	if decode(t, out)["data"].(map[string]any)["updated"].(float64) != 2 {
		t.Fatalf("overwrite = %s", out)
	}
}

func TestTagCommand(t *testing.T) {
	db := newDB(t)
	out, _, _ := run(t, "add", "--db", db, "--category", "c", "--title", "t", "--content", "b")
	id := int64(decode(t, out)["data"].(map[string]any)["id"].(float64))
	sid := itoa(id)

	run(t, "tag", "--db", db, "add", sid, "go", "sqlite")
	out, _, _ = run(t, "tag", "--db", db, "list")
	tags := decode(t, out)["data"].(map[string]any)
	if tags["go"].(float64) != 1 || tags["sqlite"].(float64) != 1 {
		t.Fatalf("tags = %s", out)
	}

	run(t, "tag", "--db", db, "rm", sid, "go")
	out, _, _ = run(t, "tag", "--db", db, "list")
	tags = decode(t, out)["data"].(map[string]any)
	if _, ok := tags["go"]; ok {
		t.Fatalf("go tag still present: %s", out)
	}
}

func TestWorkspacesCommand(t *testing.T) {
	db := newDB(t)
	run(t, "add", "--db", db, "--workspace", "a", "--category", "c", "--title", "x", "--content", "b")
	run(t, "add", "--db", db, "--workspace", "b", "--category", "c", "--title", "y", "--content", "b")

	out, _, code := run(t, "workspaces", "--db", db)
	if code != exitOK {
		t.Fatalf("code = %d", code)
	}
	rows := decode(t, out)["data"].([]any)
	if len(rows) != 2 {
		t.Fatalf("workspaces = %v", rows)
	}
}
