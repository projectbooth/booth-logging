package jwksfetch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeCore serves discovery + JWKS like core's /iframe-identity issuer; issuerOverride lets a
// test publish a different issuer string than the one it's reached at.
func fakeCore(t *testing.T, issuerOverride string, jwks string, jwksStatus int) (*httptest.Server, string) {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/iframe-identity/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		iss := srv.URL + "/iframe-identity"
		if issuerOverride != "" {
			iss = issuerOverride
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": iss, "jwks_uri": srv.URL + "/iframe-identity/.well-known/jwks.json"})
	})
	mux.HandleFunc("/iframe-identity/.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(jwksStatus)
		_, _ = w.Write([]byte(jwks))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, srv.URL + "/iframe-identity"
}

const goodJWKS = `{"keys":[{"kty":"RSA","use":"sig","kid":"k1","alg":"RS256","n":"sXch","e":"AQAB"}]}`

func TestFetch(t *testing.T) {
	_, issuer := fakeCore(t, "", goodJWKS, 200)
	got, err := Fetch(context.Background(), http.DefaultClient, issuer+"/")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != goodJWKS {
		t.Errorf("got %s", got)
	}
}

func TestFetch_IssuerMustMatchExactly(t *testing.T) {
	// ADR 0069's recorded gotcha: ".svc.cluster.local" vs ".svc".
	_, issuer := fakeCore(t, "http://booth-core.booth-system.svc:8080/iframe-identity", goodJWKS, 200)
	_, err := Fetch(context.Background(), http.DefaultClient, issuer)
	if err == nil || !strings.Contains(err.Error(), "issuer mismatch") || !strings.Contains(err.Error(), ".svc:8080") {
		t.Fatalf("err = %v, want an issuer mismatch naming both spellings", err)
	}
	// ...and it isn't retried: waiting won't fix a typo.
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := FetchWithRetry(ctx, http.DefaultClient, issuer, time.Second, t.Logf); err == nil || time.Since(start) > time.Second {
		t.Errorf("mismatch retried or accepted: %v after %v", err, time.Since(start))
	}
}

func TestFetch_RejectsUnusableKeySets(t *testing.T) {
	for name, tc := range map[string]struct {
		body   string
		status int
	}{
		"empty set":          {`{"keys":[]}`, 200},
		"only an EC key":     {`{"keys":[{"kty":"EC","crv":"P-256","x":"a","y":"b"}]}`, 200},
		"encryption key":     {`{"keys":[{"kty":"RSA","use":"enc","n":"a","e":"AQAB"}]}`, 200},
		"not json":           {`<html>`, 200},
		"core not ready yet": {`{}`, 503},
	} {
		t.Run(name, func(t *testing.T) {
			_, issuer := fakeCore(t, "", tc.body, tc.status)
			if _, err := Fetch(context.Background(), http.DefaultClient, issuer); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestFetchWithRetry_WaitsForCore(t *testing.T) {
	calls := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		if strings.HasSuffix(r.URL.Path, "openid-configuration") {
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": srv.URL + "/iframe-identity", "jwks_uri": srv.URL + "/iframe-identity/.well-known/jwks.json"})
			return
		}
		_, _ = w.Write([]byte(goodJWKS))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := FetchWithRetry(ctx, http.DefaultClient, srv.URL+"/iframe-identity", 10*time.Millisecond, t.Logf); err != nil {
		t.Fatal(err)
	}
}
