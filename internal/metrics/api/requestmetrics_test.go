package metrics

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestEveryResponseCountsAndOnly5xxCountTwice(t *testing.T) {
	m := NewRequestMetrics()
	handler := m.Wrap(echo())

	for _, code := range []int{200, 404, 500, 502} {
		handler.ServeHTTP(httptest.NewRecorder(), requestFor(code))
	}

	if m.Requests() != 4 {
		t.Errorf("Requests() = %d, want every response counted", m.Requests())
	}
	if m.ServerErrors() != 2 {
		t.Errorf("ServerErrors() = %d, want the two 5xx and nothing below", m.ServerErrors())
	}
}

func TestOneInstanceMetersEveryRouteItWraps(t *testing.T) {
	m := NewRequestMetrics()
	chat, probe := m.Wrap(echo()), m.Wrap(echo())

	chat.ServeHTTP(httptest.NewRecorder(), requestFor(200))
	probe.ServeHTTP(httptest.NewRecorder(), requestFor(200))

	if m.Requests() != 2 {
		t.Errorf("Requests() = %d, want both routes pooled in one counter", m.Requests())
	}
}

func TestRecorderUnwrapsForStreaming(t *testing.T) {
	var streams bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		streams = http.NewResponseController(w).Flush() == nil
	})

	NewRequestMetrics().Wrap(inner).ServeHTTP(httptest.NewRecorder(), requestFor(200))

	if !streams {
		t.Error("Flush must reach the real writer through the recorder — wrapping must not break streaming")
	}
}

// echo answers with the status named in the request path.
func echo() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		if err != nil {
			code = http.StatusOK
		}
		w.WriteHeader(code)
	})
}

func requestFor(code int) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/"+strconv.Itoa(code), nil)
}
