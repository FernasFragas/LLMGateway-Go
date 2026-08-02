package keys_provider

// Test harness: a fetcher the test drives directly, and a temp dir standing
// in for the mount. Each test states the rule it holds the cache to at its
// own call site.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// serving is a Fetcher returning whatever the test currently wants, counting
// reads so a test can prove a refresh did or did not happen.
type serving struct {
	mu    sync.Mutex
	key   string
	err   error
	reads int
	block chan struct{} // when non-nil, Fetch waits on it — a hung source
}

func (s *serving) Fetch(context.Context, string) (string, error) {
	s.mu.Lock()
	block := s.block
	s.mu.Unlock()

	if block != nil {
		<-block
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++

	return s.key, s.err
}

func (s *serving) set(key string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.key, s.err = key, err
}

func (s *serving) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.reads
}

// cacheOf builds a one-provider cache over src, already wired to "openai".
func cacheOf(t *testing.T, src Fetcher) *Cache {
	t.Helper()
	c, err := New(src, map[string]Source{
		"openai": {Path: "secret/data/llm/openai", RefreshInterval: time.Minute},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return c
}

// warm is a cache that has loaded its credential once.
func warm(t *testing.T, src *serving) *Cache {
	t.Helper()
	c := cacheOf(t, src)
	if err := c.RefreshAll(context.Background()); err != nil {
		t.Fatalf("first load: %v", err)
	}

	return c
}

// eventually polls until want holds or the deadline passes — the out-of-band
// refresh is asynchronous by design, so there is nothing to synchronize on.
func eventually(t *testing.T, want func() bool, because string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if want() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("condition never held: %s", because)
}

// writeSecret lands a credential file and returns its path.
func writeSecret(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "openai")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	return path
}

var errSourceDown = errors.New("secret store unreachable")

// rewrite replaces a secret file's contents in place, as a rotating mount does.
func rewrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("rewrite secret: %v", err)
	}
}

// decodeJSON reads a request body into v.
func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// observing wraps a Refresher the way a logs decorator would, remembering what
// passed through so a test can prove the seam carries both the call and its
// error.
type observing struct {
	next Refresher

	mu   sync.Mutex
	n    int
	last error
}

func (o *observing) Refresh(ctx context.Context, provider string) error {
	err := o.next.Refresh(ctx, provider)

	o.mu.Lock()
	defer o.mu.Unlock()
	o.n++
	o.last = err

	return err
}

func (o *observing) calls() int {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.n
}

func (o *observing) lastErr() error {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.last
}
