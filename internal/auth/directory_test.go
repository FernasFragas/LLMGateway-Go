package auth

// The port contract: AppForKey answers who the caller is or that nobody is —
// never why. Every refusal here is the same false the core saw for an
// unknown API key.

import (
	"context"
	"testing"
	"time"
)

func TestProjectedTokenAdmitsItsApp(t *testing.T) {
	s := newSigner(t)
	dir := directory(t, warmCache(t, s))

	app, ok := dir.AppForKey(context.Background(), s.mint(t, nil))

	if !ok || app.Name != "agent-service" {
		t.Errorf("AppForKey = (%+v, %v), want the agent-service app admitted", app, ok)
	}
}

func TestWrongAudienceIsRefused(t *testing.T) {
	s := newSigner(t)
	dir := directory(t, warmCache(t, s))

	_, ok := dir.AppForKey(context.Background(), s.mint(t, map[string]any{"aud": "some-other-service"}))

	if ok {
		t.Error("a token bound to another audience must be refused — bound means bound")
	}
}

func TestForeignIssuerIsRefused(t *testing.T) {
	s := newSigner(t)
	dir := directory(t, warmCache(t, s))

	_, ok := dir.AppForKey(context.Background(), s.mint(t, map[string]any{"iss": "https://evil.example.com"}))

	if ok {
		t.Error("a token from a foreign issuer must be refused, however valid its signature")
	}
}

func TestExpiredTokenIsRefused(t *testing.T) {
	s := newSigner(t)
	dir := directory(t, warmCache(t, s))

	_, ok := dir.AppForKey(context.Background(), s.mint(t, map[string]any{"exp": time.Now().Add(-2 * time.Minute).Unix()}))

	if ok {
		t.Error("an expired token must be refused — short-lived is the whole point")
	}
}

func TestUnknownServiceAccountIsRefused(t *testing.T) {
	s := newSigner(t)
	dir := directory(t, warmCache(t, s))

	_, ok := dir.AppForKey(context.Background(), s.mint(t, map[string]any{"sub": "system:serviceaccount:default:stranger"}))

	if ok {
		t.Error("a valid token from an unlisted ServiceAccount must be refused — identity is not authorization")
	}
}

func TestColdCacheRefusesEveryToken(t *testing.T) {
	s := newSigner(t)
	dir := directory(t, coldCache(t, jwksServer(t, s.jwks()).URL)) // never refreshed

	_, ok := dir.AppForKey(context.Background(), s.mint(t, nil))

	if ok {
		t.Error("with no keys loaded nothing can verify — the pod refuses readiness instead")
	}
}
