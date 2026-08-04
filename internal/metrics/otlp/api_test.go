package otlp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	apimetrics "github.com/FernasFragas/LLMGateway-Go/internal/metrics/api"
)

func TestRegisterAPIReportsRequestsErrorsAndPanics(t *testing.T) {
	mp, reader := meterAndReader()

	reqs, panics := apimetrics.NewRequestMetrics(), apimetrics.NewPanicCounter()
	handler := reqs.Wrap(panics.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if err := RegisterAPI(mp.Meter("test"), reqs, panics); err != nil {
		t.Fatalf("RegisterAPI: %v", err)
	}

	got := collected(t, reader)
	cases := map[string]int64{
		"http_requests_total":      1,
		"http_server_errors_total": 1,
		"http_panics_total":        0,
	}
	for name, want := range cases {
		values := got[name]
		if len(values) != 1 || values[0] != want {
			t.Errorf("%s = %v, want [%d]", name, values, want)
		}
	}
}
