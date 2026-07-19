package logs

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPanicIsLoggedWithItsStackAndRePanicked(t *testing.T) {
	log, out := captured(t)
	handler := NewPanicLogger(panicking(), log)

	v := recovered(func() { handler.ServeHTTP(httptest.NewRecorder(), chatHTTPRequest()) })

	if v != "the panic value" {
		t.Errorf("re-panicked %v, want the original value — answering belongs to the recover outside", v)
	}
	wantLogged(t, out, "handler panicked", "the panic value", "stack=", "paniclogger_test.go")
	wantNeverLogged(t, out, "sk-test-secret-key", "the caller's private prompt")
}

func TestCalmRequestsPassThroughSilently(t *testing.T) {
	log, out := captured(t)
	handler := NewPanicLogger(status(http.StatusOK), log)

	handler.ServeHTTP(httptest.NewRecorder(), chatHTTPRequest())

	wantSilence(t, out)
}

// panicking stands in for a handler bug; the logged stack must name this
// file — the panic's origin — not just the decorator's own frames.
func panicking() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("the panic value")
	})
}

// recovered runs fn and returns what it panicked with, nil if it didn't.
func recovered(fn func()) (v any) {
	defer func() { v = recover() }()
	fn()
	return nil
}
