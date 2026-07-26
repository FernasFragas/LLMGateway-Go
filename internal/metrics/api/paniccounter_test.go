package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPanicIsCountedAndRePanicked(t *testing.T) {
	c := NewPanicCounter()
	handler := c.Wrap(panicking())

	v := recovered(func() { handler.ServeHTTP(httptest.NewRecorder(), requestFor(200)) })

	if v != "the panic value" {
		t.Errorf("re-panicked %v, want the original value — answering belongs to the recover outside", v)
	}
	if c.Panics() != 1 {
		t.Errorf("Panics() = %d, want the one panic counted", c.Panics())
	}
}

func TestCalmRequestsCountNothing(t *testing.T) {
	c := NewPanicCounter()

	c.Wrap(echo()).ServeHTTP(httptest.NewRecorder(), requestFor(200))

	if c.Panics() != 0 {
		t.Errorf("Panics() = %d, want zero for a calm request", c.Panics())
	}
}

// panicking stands in for a handler bug.
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
