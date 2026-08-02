package keys_provider

import (
	"context"
	"path/filepath"
	"testing"
)

func TestFileFetchTrimsTheTrailingNewline(t *testing.T) {
	// Every editor and `kubectl create secret --from-file` leaves one, and a
	// credential sent with a newline is a credential the provider rejects.
	path := writeSecret(t, "sk-live\n")

	got, err := NewFile().Fetch(context.Background(), path)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got != "sk-live" {
		t.Errorf("Fetch = %q, want the credential without its trailing newline", got)
	}
}

func TestFileFetchReportsAMissingMount(t *testing.T) {
	_, err := NewFile().Fetch(context.Background(), filepath.Join(t.TempDir(), "absent"))
	if err == nil {
		t.Fatal("a path that does not exist must be reported, so fail-static keeps the old key and the decorator logs why")
	}
}

func TestFileFetchRereadsRotatedContent(t *testing.T) {
	// A mounted Secret rotates in place; re-reading is how rotation arrives.
	path := writeSecret(t, "sk-first")
	src := NewFile()

	if got, _ := src.Fetch(context.Background(), path); got != "sk-first" {
		t.Fatalf("Fetch = %q, want the original", got)
	}

	rewrite(t, path, "sk-rotated")

	if got, _ := src.Fetch(context.Background(), path); got != "sk-rotated" {
		t.Errorf("Fetch = %q, want the rotated credential — the fetcher must hold nothing between reads", got)
	}
}
