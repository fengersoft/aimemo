package cli

import (
	"bytes"
	"encoding/json"
	"os"
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
