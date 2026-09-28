// Command stubcore stands in for booth-core's iframe-identity issuer (ADR 0069) in the kind
// integration run, which has no real booth-core: it generates a signing key and writes the
// issuer's two well-known documents plus a few signed X-Booth-Identity assertions shaped
// exactly like core's (iframeidentity.Service.Mint). verify.sh serves the documents from a
// throwaway pod so the Grafana pod's fetch-jwks init container fetches real keys, then sends the
// assertions to Grafana.
//
//	go run ./test/integration/stubcore -issuer http://stub-core.ns.svc.cluster.local:8080/iframe-identity -out DIR
//
// Writes DIR/openid-configuration, DIR/jwks.json, and DIR/<role>.jwt for owner/editor/viewer in
// workspace "acme". Assertions live 30 minutes (core's live 2) so the run has time to use them.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"flag"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func main() {
	issuer := flag.String("issuer", "", "issuer URL the stub serves at")
	out := flag.String("out", "", "output directory")
	aud := flag.String("aud", "logging-grafana", "module id the assertions are for")
	flag.Parse()
	if *issuer == "" || *out == "" {
		log.Fatal("-issuer and -out are required")
	}
	iss := strings.TrimRight(*issuer, "/")

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatal(err)
	}
	write := func(name string, v any) {
		var b []byte
		if s, ok := v.(string); ok {
			b = []byte(s)
		} else if b, err = json.Marshal(v); err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(*out, name), b, 0o644); err != nil {
			log.Fatal(err)
		}
	}
	write("openid-configuration", map[string]any{
		"issuer": iss, "jwks_uri": iss + "/.well-known/jwks.json",
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
	write("jwks.json", jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "stub-1", Algorithm: "RS256", Use: "sig"}}})

	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "stub-1"))
	if err != nil {
		log.Fatal(err)
	}
	now := time.Now()
	for _, role := range []string{"owner", "editor", "viewer"} {
		jti := make([]byte, 8)
		_, _ = rand.Read(jti)
		tok, err := jwt.Signed(signer).Claims(jwt.Claims{
			Issuer: iss, Subject: "it-" + role, Audience: jwt.Audience{*aud},
			IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
			Expiry: jwt.NewNumericDate(now.Add(30 * time.Minute)), ID: hex.EncodeToString(jti),
		}).Claims(map[string]any{"groups": []string{"/workspaces/acme/" + role}, "booth_module": *aud}).Serialize()
		if err != nil {
			log.Fatal(err)
		}
		write(role+".jwt", tok)
	}
}
