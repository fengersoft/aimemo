package cli

import (
	"bytes"
	"encoding/json"
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
