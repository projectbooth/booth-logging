// Package jwksfetch fetches booth-core's iframe-identity signing keys for the Grafana view
// (ADR 0076), run as the Grafana pod's init container (`booth-logging fetch-jwks`).
//
// Why a file at all: Grafana's JWT auth only fetches a JWKS URL over https (it refuses an http
// jwk_set_url outside its development mode), and core publishes its iframe-identity issuer on
// its plain-http in-cluster Service (ADR 0069). So the keys are fetched here, once per pod
// start, and Grafana reads them from jwk_set_file. Core generates this key once and keeps it in
// a Secret; it only changes if that Secret is deleted, and then Grafana refuses every login
// (fails closed) until its pod is restarted and fetches again.
//
// It also checks what ADR 0069's implementation notes warn about: the discovery document's
// `issuer` must equal the configured issuer URL exactly (core's `…svc.cluster.local:8080`
// versus the `…svc:8080` other booth URLs use), or every assertion's `iss` would be refused.
// Better to fail here, naming both spellings, than to start a Grafana that admits nobody.
package jwksfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const discoveryPath = "/.well-known/openid-configuration"

// maxBody bounds what is read from core; a JWKS is a few KB.
const maxBody = 1 << 20

// Fetch reads issuer's discovery document, checks its issuer, and returns its JWKS — validated
// to hold at least one RSA signing key — as the raw JSON Grafana will read.
func Fetch(ctx context.Context, client *http.Client, issuer string) ([]byte, error) {
	issuer = strings.TrimRight(issuer, "/")
	var disc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	raw, err := get(ctx, client, issuer+discoveryPath)
	if err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}
	if err := json.Unmarshal(raw, &disc); err != nil {
		return nil, fmt.Errorf("discovery document is not JSON: %w", err)
	}
	if disc.Issuer != issuer {
		return nil, fmt.Errorf("issuer mismatch: configured %q but core's discovery document says %q — they must be identical, character for character (grafana.identity.issuerUrl)", issuer, disc.Issuer)
	}
	if disc.JWKSURI == "" {
		return nil, errors.New("discovery document has no jwks_uri")
	}

	keys, err := get(ctx, client, disc.JWKSURI)
	if err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(keys, &set); err != nil {
		return nil, fmt.Errorf("jwks is not JSON: %w", err)
	}
	usable := 0
	for _, k := range set.Keys {
		if k.Kty == "RSA" && (k.Use == "" || k.Use == "sig") && k.N != "" && k.E != "" {
			usable++
		}
	}
	if usable == 0 {
		return nil, fmt.Errorf("jwks at %s holds no RSA signing key", disc.JWKSURI)
	}
	return keys, nil
}

// FetchWithRetry retries Fetch until it succeeds or ctx ends: core may still be starting when
// the Grafana pod does. An issuer mismatch is not retried — waiting won't fix a typo.
func FetchWithRetry(ctx context.Context, client *http.Client, issuer string, every time.Duration, logf func(string, ...any)) ([]byte, error) {
	for {
		keys, err := Fetch(ctx, client, issuer)
		if err == nil {
			return keys, nil
		}
		if strings.HasPrefix(err.Error(), "issuer mismatch") {
			return nil, err
		}
		logf("fetching core's iframe-identity keys from %s: %v (retrying)", issuer, err)
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("gave up: %w", err)
		case <-time.After(every):
		}
	}
}

func get(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, res.StatusCode)
	}
	return body, nil
}
