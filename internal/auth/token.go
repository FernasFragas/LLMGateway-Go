package auth

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// leeway absorbs clock skew between the token's signer and this gateway.
// Projected tokens live for hours; half a minute forgives clocks, not
// expiry.
const leeway = 30 * time.Second

// header is the JOSE header — only what the allowlist needs.
type header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

// claims are the registered claims this verifier judges. Everything else in
// the token is ignored: the subject is the identity, the rest is the
// cluster's business.
type claims struct {
	Issuer    string   `json:"iss"`
	Subject   string   `json:"sub"`
	Audience  audience `json:"aud"`
	Expiry    int64    `json:"exp"`
	NotBefore int64    `json:"nbf"`
}

// audience accepts the claim's string-or-array shape.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var one string
		if err := json.Unmarshal(b, &one); err != nil {
			return err
		}
		*a = audience{one}

		return nil
	}

	return json.Unmarshal(b, (*[]string)(a))
}

func (a audience) contains(want string) bool {
	for _, aud := range a {
		if aud == want {
			return true
		}
	}

	return false
}

// verifyToken checks one compact JWT against the cached keys and the
// configured issuer and audience, and returns its subject. The algorithm
// allowlist is exactly RS256 — anything else, "none" included, is refused
// before a single claim is read.
func verifyToken(raw string, keyFor func(kid string) (*rsa.PublicKey, bool), issuer, aud string, now time.Time) (string, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return "", errors.New("token is not a compact JWT")
	}

	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("header: %w", err)
	}
	var h header
	if err := json.Unmarshal(headerJSON, &h); err != nil {
		return "", fmt.Errorf("header: %w", err)
	}
	if h.Alg != "RS256" {
		return "", fmt.Errorf("algorithm %q is not allowed", h.Alg)
	}
	if h.Kid == "" {
		return "", errors.New("header names no key")
	}

	pub, ok := keyFor(h.Kid)
	if !ok {
		return "", fmt.Errorf("no key for kid %q", h.Kid)
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("signature: %w", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		return "", errors.New("signature does not verify")
	}

	// Only now — with the signature proven — are the claims worth reading.
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("claims: %w", err)
	}
	var c claims
	if err := json.Unmarshal(claimsJSON, &c); err != nil {
		return "", fmt.Errorf("claims: %w", err)
	}

	switch {
	case c.Issuer != issuer:
		return "", fmt.Errorf("issuer %q is not this cluster's", c.Issuer)
	case !c.Audience.contains(aud):
		return "", errors.New("token is not bound to this gateway's audience")
	case c.Expiry == 0:
		return "", errors.New("token carries no expiry")
	case now.After(time.Unix(c.Expiry, 0).Add(leeway)):
		return "", errors.New("token is expired")
	case c.NotBefore != 0 && now.Add(leeway).Before(time.Unix(c.NotBefore, 0)):
		return "", errors.New("token is not yet valid")
	case c.Subject == "":
		return "", errors.New("token names no subject")
	}

	return c.Subject, nil
}
