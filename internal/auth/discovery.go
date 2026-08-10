package auth

// The cluster's OIDC discovery document, read once at boot to check the one
// configured value whose mistake is otherwise invisible.
//
// auth.issuer is matched exactly against every token's iss claim, and the
// value that works on Docker Desktop is wrong on every managed cluster,
// where tokens are issued by an OIDC URL. Deploy the shipped default to EKS
// and the pod becomes ready, passes every probe, and refuses 100% of callers
// with a log line that blames their token. The apiserver publishes the truth
// at /.well-known/openid-configuration and the gateway can already read it —
// the same ClusterRoleBinding that grants the JWKS path covers this one — so
// the check costs one request at startup and turns a silent outage into a
// boot failure that names both values.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// wellKnownPath is where an OIDC provider publishes its metadata, fixed by
// the spec — which is why it is derived from the JWKS URL's origin rather
// than being a fourth thing to configure and keep in step.
const wellKnownPath = "/.well-known/openid-configuration"

// maxDiscoveryBytes caps the document for the same reason maxJWKSBytes caps
// the keyset: a typo'd URL pointing at some other service must not stream an
// unbounded body into the process.
const maxDiscoveryBytes = 1 << 20

// IssuerMismatch is a configured issuer that is not the one this cluster
// signs tokens with. It is separate from every other failure here because it
// is the only one that is fatal: an unreachable endpoint leaves the question
// open, but a mismatch is a definite answer, and serving on it means
// refusing every caller.
type IssuerMismatch struct {
	Configured string
	Cluster    string
}

func (e *IssuerMismatch) Error() string {
	return fmt.Sprintf(
		"auth: configured issuer %q is not this cluster's (%q) — every caller would be refused 401; set auth.issuer to the cluster value",
		e.Configured, e.Cluster,
	)
}

// VerifyIssuer checks the configured issuer against what the cluster
// advertises.
//
// It returns *IssuerMismatch when they disagree, and an ordinary error when
// the document could not be read at all. Callers are meant to treat those
// differently: a mismatch is a boot failure, while an unreachable endpoint
// is not — the JWKS fetch behind it is failing too, so readiness already
// holds traffic off this pod, and refusing to boot would turn a recoverable
// apiserver blip into a crash loop.
//
// Only the issuer is compared. jwks_uri is deliberately not: the document
// advertises the apiserver's own address (an OIDC URL on a managed cluster,
// a node IP on Docker Desktop) while the gateway is configured with the
// in-cluster service path, and those differing is the normal, correct setup
// rather than a misconfiguration.
func VerifyIssuer(ctx context.Context, client *http.Client, jwksURL, issuer string) error {
	discoveryURL, err := DiscoveryURL(jwksURL)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return fmt.Errorf("auth: build discovery request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("auth: fetch discovery document: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("auth: fetch discovery document: status %d", resp.StatusCode)
	}

	var doc struct {
		Issuer string `json:"issuer"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxDiscoveryBytes)).Decode(&doc); err != nil {
		return fmt.Errorf("auth: decode discovery document: %w", err)
	}
	if doc.Issuer == "" {
		return errors.New("auth: discovery document advertises no issuer")
	}

	if doc.Issuer != issuer {
		return &IssuerMismatch{Configured: issuer, Cluster: doc.Issuer}
	}

	return nil
}

// DiscoveryURL derives the metadata document from the JWKS URL's origin.
// Both are served by the apiserver, so one configured URL is enough — and a
// second would only be something to keep in step with the first.
func DiscoveryURL(jwksURL string) (string, error) {
	parsed, err := url.Parse(jwksURL)
	if err != nil {
		return "", fmt.Errorf("auth: parse JWKS URL %q: %w", jwksURL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("auth: JWKS URL %q must be absolute", jwksURL)
	}

	return parsed.Scheme + "://" + parsed.Host + wellKnownPath, nil
}
