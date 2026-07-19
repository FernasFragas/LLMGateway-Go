package auth

// The verifier's own clauses: the algorithm allowlist and the integrity of
// the compact form — what must hold before any claim is believed.

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAlgorithmAllowlistRefusesNoneAndHMAC(t *testing.T) {
	s := newSigner(t)
	dir := directory(t, warmCache(t, s))

	for name, header := range map[string]map[string]any{
		"alg none":  {"alg": "none", "kid": s.kid},
		"alg HS256": {"alg": "HS256", "kid": s.kid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := dir.AppForKey(context.Background(), s.mintWithHeader(t, header, nil)); ok {
				t.Error("only RS256 is allowed — every other algorithm is an attack surface, not a feature")
			}
		})
	}
}

func TestTamperedClaimsAreRefused(t *testing.T) {
	s := newSigner(t)
	dir := directory(t, warmCache(t, s))

	honest := s.mint(t, nil)
	forged := s.mint(t, map[string]any{"sub": "system:serviceaccount:llm:impostor"})
	parts, forgedParts := strings.Split(honest, "."), strings.Split(forged, ".")
	spliced := parts[0] + "." + forgedParts[1] + "." + parts[2] // honest signature, forged claims

	if _, ok := dir.AppForKey(context.Background(), spliced); ok {
		t.Error("claims changed after signing must not verify")
	}
}

func TestNotYetValidTokenIsRefused(t *testing.T) {
	s := newSigner(t)
	dir := directory(t, warmCache(t, s))

	_, ok := dir.AppForKey(context.Background(), s.mint(t, map[string]any{"nbf": time.Now().Add(time.Hour).Unix()}))

	if ok {
		t.Error("a token from the future must be refused")
	}
}

func TestUnknownKidIsRefused(t *testing.T) {
	s := newSigner(t)
	dir := directory(t, warmCache(t, s))

	_, ok := dir.AppForKey(context.Background(), s.mintWithHeader(t, map[string]any{"alg": "RS256", "kid": "rotated-away"}, nil))

	if ok {
		t.Error("a kid the JWKS never published has no key to verify against")
	}
}

func TestGarbageIsRefusedNotPanicked(t *testing.T) {
	s := newSigner(t)
	dir := directory(t, warmCache(t, s))

	for _, raw := range []string{"", "not-a-jwt", "a.b", "a.b.c.d", "!!!.???.###"} {
		if _, ok := dir.AppForKey(context.Background(), raw); ok {
			t.Errorf("%q must be refused", raw)
		}
	}
}
