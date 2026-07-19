package logs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestLogNamesTheRequestNeverTheSecrets(t *testing.T) {
	log, out := captured(t)
	handler := NewRequestLogger(correlatedOK(), log)

	handler.ServeHTTP(httptest.NewRecorder(), chatHTTPRequest())

	wantLogged(t, out, "request served", "POST", "/v1/chat", "status=200", "duration_ms=", "correlation_id=req-42")
	wantNeverLogged(t, out, "sk-test-secret-key", "the caller's private prompt")
}

func TestRequestLogRecordsTheWrittenStatus(t *testing.T) {
	log, out := captured(t)
	handler := NewRequestLogger(status(http.StatusBadGateway), log)

	handler.ServeHTTP(httptest.NewRecorder(), chatHTTPRequest())

	wantLogged(t, out, "status=502")
}

func TestRecorderUnwrapsForStreaming(t *testing.T) {
	log, _ := captured(t)
	var streams bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		streams = http.NewResponseController(w).Flush() == nil
	})

	NewRequestLogger(inner, log).ServeHTTP(httptest.NewRecorder(), chatHTTPRequest())

	if !streams {
		t.Error("Flush must reach the real writer through the recorder — wrapping must not break streaming")
	}
}

// correlatedOK answers 200 after echoing a correlation ID, as the edge's
// correlationID middleware does before this decorator runs.
func correlatedOK() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Correlation-ID", "req-42")
		w.WriteHeader(http.StatusOK)
	})
}

// status answers with the given code.
func status(code int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(code)
	})
}

// chatHTTPRequest carries a credential and recognizable private content so
// content-blindness assertions have something to catch.
func chatHTTPRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(`{"messages":[{"role":"user","content":"the caller's private prompt"}]}`))
	req.Header.Set("Authorization", "Bearer sk-test-secret-key")
	return req
}
