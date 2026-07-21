package config

// Test harness: files written to a test dir, loaded back. The happy path is
// config.example.yaml itself — the executable schema its header promises;
// each violation test states its own minimal file.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exampleFile is the shipped example, parsed in CI so it cannot drift.
const exampleFile = "../../config.example.yaml"

// write lands the given content as a config file and returns its path.
func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gateway.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// wantBootFailure asserts Load refuses the file, naming the mistake.
func wantBootFailure(t *testing.T, content, wantInError string) {
	t.Helper()
	_, err := Load(write(t, content))
	if err == nil {
		t.Fatal("Load accepted a file that must fail the boot")
	}
	if !strings.Contains(err.Error(), wantInError) {
		t.Errorf("error %q does not name the mistake %q", err, wantInError)
	}
}
