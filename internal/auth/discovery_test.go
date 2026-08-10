package auth

// The boot-time issuer check: a mismatch is certain and fatal, everything
// else only leaves the question open.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAMatchingIssuerPassesTheCheck(t *testing.T) {
	url := discoveryServing(t, `{"issuer":"https://kubernetes.default.svc.cluster.local"}`)

	err := VerifyIssuer(context.Background(), http.DefaultClient, url, "https://kubernetes.default.svc.cluster.local")
	if err != nil {
		t.Errorf("VerifyIssuer = %v, want nil when the configured issuer is the cluster's", err)
	}
}

func TestAWrongIssuerIsReportedAsAMismatchCarryingBothValues(t *testing.T) {
	// The whole point: this is the misconfiguration that otherwise passes
	// every probe and refuses every caller. The error has to name both values
	// so the fix is copy-paste rather than a search.
	url := discoveryServing(t, `{"issuer":"https://oidc.eks.eu-west-1.amazonaws.com/id/EXAMPLE"}`)

	err := VerifyIssuer(context.Background(), http.DefaultClient, url, "https://kubernetes.default.svc.cluster.local")

	var mismatch *IssuerMismatch
	if !errors.As(err, &mismatch) {
		t.Fatalf("VerifyIssuer = %v, want an *IssuerMismatch", err)
	}
	if mismatch.Cluster != "https://oidc.eks.eu-west-1.amazonaws.com/id/EXAMPLE" {
		t.Errorf("Cluster = %q, want the value the cluster advertises", mismatch.Cluster)
	}
	if mismatch.Configured != "https://kubernetes.default.svc.cluster.local" {
		t.Errorf("Configured = %q, want the value the operator set", mismatch.Configured)
	}
}

func TestAnUnreachableClusterIsNotAMismatch(t *testing.T) {
	// The distinction main depends on. A mismatch is a definite answer and a
	// boot failure; an unreachable document is no answer at all, and crashing
	// on it would turn an apiserver blip into a crash loop while readiness is
	// already holding traffic off this pod.
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()

	err := VerifyIssuer(context.Background(), http.DefaultClient, srv.URL+"/openid/v1/jwks", "https://anything")

	if err == nil {
		t.Fatal("an unreachable discovery endpoint must be reported")
	}
	var mismatch *IssuerMismatch
	if errors.As(err, &mismatch) {
		t.Error("an unreachable endpoint was classified as a mismatch — that would make a blip fatal")
	}
}

func TestANonOKDiscoveryResponseIsNotAMismatch(t *testing.T) {
	// RBAC is the likely cause of a 403 here, and it is the same binding the
	// JWKS fetch needs — so readiness will refuse anyway. Not a boot failure.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	err := VerifyIssuer(context.Background(), http.DefaultClient, srv.URL+"/openid/v1/jwks", "https://anything")

	var mismatch *IssuerMismatch
	if err == nil || errors.As(err, &mismatch) {
		t.Errorf("VerifyIssuer = %v, want a plain error for a 403", err)
	}
}

func TestADocumentWithNoIssuerIsNotAMismatch(t *testing.T) {
	// Answering something that is not a discovery document proves nothing
	// about the configured issuer, so it must not condemn it.
	url := discoveryServing(t, `{"response_types_supported":["id_token"]}`)

	err := VerifyIssuer(context.Background(), http.DefaultClient, url, "https://anything")

	var mismatch *IssuerMismatch
	if err == nil || errors.As(err, &mismatch) {
		t.Errorf("VerifyIssuer = %v, want a plain error when no issuer is advertised", err)
	}
}

func TestJWKSURIIsNotComparedAgainstTheConfiguredOne(t *testing.T) {
	// A real cluster advertises the apiserver's own address here — an OIDC
	// URL on EKS, a node IP on Docker Desktop — while the gateway is
	// configured with the in-cluster service path. Those differing is the
	// correct setup, so comparing them would fail a working deployment.
	url := discoveryServing(t, `{
		"issuer":"https://kubernetes.default.svc.cluster.local",
		"jwks_uri":"https://172.19.0.3:6443/openid/v1/jwks"
	}`)

	if err := VerifyIssuer(context.Background(), http.DefaultClient, url, "https://kubernetes.default.svc.cluster.local"); err != nil {
		t.Errorf("VerifyIssuer = %v, want nil — jwks_uri differing from the configured URL is normal", err)
	}
}

func TestTheDiscoveryURLComesFromTheJWKSOrigin(t *testing.T) {
	// One configured URL rather than two that could drift apart.
	got, err := DiscoveryURL("https://kubernetes.default.svc/openid/v1/jwks")
	if err != nil {
		t.Fatalf("DiscoveryURL: %v", err)
	}
	if want := "https://kubernetes.default.svc/.well-known/openid-configuration"; got != want {
		t.Errorf("DiscoveryURL = %q, want %q", got, want)
	}
}

func TestARelativeJWKSURLIsRefused(t *testing.T) {
	if _, err := DiscoveryURL("/openid/v1/jwks"); err == nil {
		t.Error("a relative JWKS URL yields no origin to derive the discovery document from")
	}
}

// discoveryServing starts an apiserver stub answering the well-known path
// with body, and returns a JWKS URL on the same origin — the shape
// VerifyIssuer takes.
func discoveryServing(t *testing.T, body string) string {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc(wellKnownPath, func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write discovery document: %v", err)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv.URL + "/openid/v1/jwks"
}
