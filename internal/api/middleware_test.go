package api

// The middleware contract: what every route promises regardless of
// endpoint — correlation on every response, panics answered not fatal,
// credentials extracted but never logged.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/FernasFragas/LLMGateway-Go/internal/gateway"
)

func TestNoCredentialNoCore(t *testing.T) {
	core := &chatStub{result: served()}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(validChatBody))

	rec := do(serving(t, core), req) // no Authorization header

	wantErrorCode(t, rec, http.StatusUnauthorized, "unauthorized")
	if core.calls != 0 {
		t.Error("a request with no credential must never reach the core")
	}
}

func TestClientCorrelationIDIsAdopted(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(validChatBody))
	req.Header.Set("Authorization", "Bearer "+testKey)
	req.Header.Set("X-Correlation-ID", "caller-supplied-id")

	rec := do(serving(t, &chatStub{result: served()}), req)

	if got := rec.Header().Get("X-Correlation-ID"); got != "caller-supplied-id" {
		t.Errorf("X-Correlation-ID = %q, want the caller's id echoed", got)
	}
}

func TestMissingCorrelationIDIsGenerated(t *testing.T) {
	rec := postChat(serving(t, &chatStub{result: served()}), validChatBody)

	if rec.Header().Get("X-Correlation-ID") == "" {
		t.Error("every response must carry X-Correlation-ID, generated when absent")
	}
}

func TestOverlongCorrelationIDIsReplaced(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat", strings.NewReader(validChatBody))
	req.Header.Set("Authorization", "Bearer "+testKey)
	req.Header.Set("X-Correlation-ID", strings.Repeat("x", 129))

	rec := do(serving(t, &chatStub{result: served()}), req)

	got := rec.Header().Get("X-Correlation-ID")
	if got == "" || strings.Contains(got, "xxx") {
		t.Errorf("X-Correlation-ID = %q, want a fresh id — a partial one correlates with nothing", got)
	}
}

func TestPanicCostsOneRequestNotTheProcess(t *testing.T) {
	rec := postChat(serving(t, &panickingCore{}), validChatBody)

	wantErrorCode(t, rec, http.StatusInternalServerError, "internal_error")
	if strings.Contains(rec.Body.String(), "the panic value") {
		t.Error("the panic value must never reach the caller")
	}
}

func TestPanicSeamSeesThePanicAndTheCallerStillGetsTheErrorBody(t *testing.T) {
	seam := &panicSeam{}
	srv := servingObserved(t, &panickingCore{}, nil, []Middleware{seam.wrap})

	rec := postChat(srv, validChatBody)

	wantErrorCode(t, rec, http.StatusInternalServerError, "internal_error")
	if seam.seen != "the panic value" {
		t.Errorf("panic seam saw %v, want the panic itself — it must sit inside recoverPanic", seam.seen)
	}
}

func TestRequestSeamSeesEveryResponsePanicAnswersIncluded(t *testing.T) {
	seam := &requestSeam{}
	srv := servingObserved(t, &panickingCore{}, []Middleware{seam.wrap}, nil)

	postChat(srv, validChatBody)

	if !seam.completed {
		t.Error("request seam must complete even for a panicked request — it sits outside recoverPanic")
	}
}

func TestProbesSkipTheRequestSeamButKeepThePanicSeam(t *testing.T) {
	req := &requestSeam{}
	pan := &panicSeam{}
	srv := servingObserved(t, &chatStub{}, []Middleware{req.wrap}, []Middleware{pan.wrap})

	get(srv, "/healthz")

	if req.completed {
		t.Error("probes must skip the request seam — they would drown the request log")
	}
	if !pan.wrapped {
		t.Error("probes must keep the panic seam — their panics are observed too")
	}
}

// panickingCore stands in for a handler bug.
type panickingCore struct{}

func (p *panickingCore) Chat(context.Context, string, gateway.ChatRequest) (gateway.ChatResult, error) {
	panic("the panic value")
}

// panicSeam honors the PanicMiddleware contract — observe, re-panic — and
// records what it saw.
type panicSeam struct {
	wrapped bool
	seen    any
}

func (s *panicSeam) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.wrapped = true
		defer func() {
			if v := recover(); v != nil {
				s.seen = v
				panic(v)
			}
		}()

		next.ServeHTTP(w, r)
	})
}

// requestSeam records whether a request passed all the way through it.
type requestSeam struct {
	completed bool
}

func (s *requestSeam) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		s.completed = true
	})
}
