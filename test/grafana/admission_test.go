// Package grafana tests the Grafana view's admission gate (ADR 0076 / ADR 0067) against a real
// Grafana running the chart's exact rendered grafana.ini, not a reading of Grafana's docs.
//
// The test plays booth-core's part: a stand-in iframe-identity issuer publishes a JWKS and
// signs X-Booth-Identity assertions shaped exactly like core's (internal/iframeidentity Mint:
// RS256, iss, sub, aud = module id, iat/nbf/exp, jti, and a one-entry groups claim for the
// active workspace). Its keys reach Grafana the way the chart's init container delivers them
// (internal/jwksfetch → jwk_set_file). Grafana runs in docker on the test Loki's network, with the chart's
// read-only root filesystem, and reaches Loki at the same URL its provisioned data source uses
// in-cluster.
//
// Needs docker, helm and hack/docker-compose.loki.yml's Loki (BOOTH_TEST_LOKI_URL). Skips
// without them locally; BOOTH_TEST_STRICT=1 (CI) turns that into a failure.
package grafana

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"gopkg.in/yaml.v3"

	"github.com/projectbooth/booth-logging/internal/jwksfetch"
	"github.com/projectbooth/booth-logging/internal/loki/lokitest"
)

const (
	moduleID    = "logging-grafana"
	lokiNetwork = "booth-logging-test_default" // hack/docker-compose.loki.yml's project network
)

var chartDir = filepath.Join("..", "..", "charts", "booth-logging")

func need(t *testing.T, bin string) {
	t.Helper()
	if _, err := exec.LookPath(bin); err != nil {
		if os.Getenv("BOOTH_TEST_STRICT") == "1" {
			t.Fatalf("%s not installed", bin)
		}
		t.Skipf("%s not installed", bin)
	}
}

// ---- stand-in for booth-core's iframe-identity issuer ------------------------------------

type issuer struct {
	url string
	key *rsa.PrivateKey
}

// newIssuer serves discovery + JWKS under core's own /iframe-identity layout. Grafana never
// contacts it: like the chart's init container, the test fetches the JWKS with
// internal/jwksfetch and hands Grafana the file.
func newIssuer(t *testing.T) *issuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	iss := &issuer{url: fmt.Sprintf("http://%s/iframe-identity", ln.Addr()), key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/iframe-identity/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": iss.url, "jwks_uri": iss.url + "/.well-known/jwks.json"})
	})
	mux.HandleFunc("/iframe-identity/.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "core-1", Algorithm: "RS256", Use: "sig"}}})
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return iss
}

type assertion struct {
	sub, workspace, role string
	aud, iss             string
	groups               any // overrides the groups claim when non-nil
	ttl                  time.Duration
	key                  *rsa.PrivateKey
}

// mint signs an assertion the way core's iframeidentity.Service.Mint does.
func (i *issuer) mint(t *testing.T, a assertion) string {
	t.Helper()
	if a.aud == "" {
		a.aud = moduleID
	}
	if a.iss == "" {
		a.iss = i.url
	}
	if a.ttl == 0 {
		a.ttl = 2 * time.Minute
	}
	key := a.key
	if key == nil {
		key = i.key
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "core-1"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	jti := make([]byte, 8)
	_, _ = rand.Read(jti)
	var groups any = []string{fmt.Sprintf("/workspaces/%s/%s", a.workspace, a.role)}
	if a.groups != nil {
		groups = a.groups
	}
	raw, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: a.iss, Subject: a.sub, Audience: jwt.Audience{a.aud},
		IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now.Add(-time.Second)),
		Expiry: jwt.NewNumericDate(now.Add(a.ttl)), ID: hex.EncodeToString(jti),
	}).Claims(map[string]any{"groups": groups, "booth_module": moduleID}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// ---- a real Grafana running the chart's rendered config -----------------------------------

type grafana struct {
	base string
}

// startGrafana renders the chart with extra values and runs Grafana with exactly its
// grafana.ini and datasources.yaml, under the chart's read-only root filesystem.
func startGrafana(t *testing.T, iss *issuer, extra ...string) *grafana {
	t.Helper()
	need(t, "helm")
	need(t, "docker")
	if err := exec.Command("docker", "network", "inspect", lokiNetwork).Run(); err != nil {
		if os.Getenv("BOOTH_TEST_STRICT") == "1" {
			t.Fatalf("docker network %s missing: start hack/docker-compose.loki.yml", lokiNetwork)
		}
		t.Skipf("docker network %s missing: start hack/docker-compose.loki.yml", lokiNetwork)
	}

	args := append([]string{"template", "booth-logging", chartDir, "--namespace", "booth-logging",
		"--set", "oidc.issuerUrl=https://idp.example", "--set", "oidc.clientId=booth-logging",
		"--set", "grafana.identity.issuerUrl=" + iss.url,
		"--show-only", "templates/grafana-config.yaml"}, extra...)
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	var cm struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(out, &cm); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, f := range []string{"grafana.ini", "datasources.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(cm.Data[f]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// What the pod's fetch-jwks init container does.
	keys, err := jwksfetch.Fetch(context.Background(), http.DefaultClient, iss.url)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "jwks.json"), keys, 0o644); err != nil {
		t.Fatal(err)
	}

	name := "booth-logging-grafana-test-" + hex.EncodeToString([]byte(t.Name()))[:12] + fmt.Sprint(time.Now().UnixNano()%100000)
	run := exec.Command("docker", "run", "-d", "--rm", "--name", name,
		"--network", lokiNetwork,
		"--read-only", "--tmpfs", "/var/lib/grafana:uid=472,gid=0", "--tmpfs", "/tmp",
		"-p", "127.0.0.1::3000",
		"-v", filepath.Join(dir, "grafana.ini")+":/etc/grafana/grafana.ini:ro",
		"-v", filepath.Join(dir, "datasources.yaml")+":/etc/grafana/provisioning/datasources/datasources.yaml:ro",
		"-v", filepath.Join(dir, "jwks.json")+":/var/lib/booth-jwks/jwks.json:ro",
		"grafana/grafana:"+grafanaTag(t))
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := exec.Command("docker", "logs", "--tail", "40", name).CombinedOutput()
			t.Logf("grafana logs:\n%s", logs)
		}
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})
	portOut, err := exec.Command("docker", "port", name, "3000").Output()
	if err != nil {
		t.Fatal(err)
	}
	g := &grafana{base: "http://" + strings.TrimSpace(strings.Split(string(portOut), "\n")[0])}

	deadline := time.Now().Add(2 * time.Minute)
	for {
		res, err := http.Get(g.base + "/api/health")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				return g
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("grafana never became healthy: %v", err)
		}
		time.Sleep(time.Second)
	}
}

// grafanaTag reads the image tag the chart deploys, so the test runs the same Grafana.
func grafanaTag(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(chartDir, "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Grafana struct {
			Image struct {
				Tag string `yaml:"tag"`
			} `yaml:"image"`
		} `yaml:"grafana"`
	}
	if err := yaml.Unmarshal(raw, &v); err != nil || v.Grafana.Image.Tag == "" {
		t.Fatalf("reading grafana.image.tag: %v", err)
	}
	return v.Grafana.Image.Tag
}

type response struct {
	status int
	body   []byte
}

func (g *grafana) do(t *testing.T, method, path, token string, body any) response {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, g.base+path, rd)
	if token != "" {
		req.Header.Set("X-Booth-Identity", token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		// What a browser inside the shell sends; Grafana's CSRF check compares it with Host.
		req.Header.Set("Origin", g.base)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return response{res.StatusCode, b}
}

// orgRole returns the Grafana role the token's person holds, or "" if Grafana refused them.
func (g *grafana) orgRole(t *testing.T, token string) (string, response) {
	t.Helper()
	r := g.do(t, "GET", "/api/user/orgs", token, nil)
	if r.status != 200 {
		return "", r
	}
	var orgs []struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(r.body, &orgs); err != nil || len(orgs) != 1 {
		t.Fatalf("orgs = %s", r.body)
	}
	return orgs[0].Role, r
}

func uniqueSub(t *testing.T) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "user-" + hex.EncodeToString(b)
}

// ---- the tests ----------------------------------------------------------------------------

// Default policy (access.workspaces empty): the owner of the active workspace is admitted as
// Editor and can query Loki; everyone and everything else is refused outright.
func TestAdmission_DefaultPolicy(t *testing.T) {
	lokiURL := lokitest.URL(t)
	iss := newIssuer(t)
	g := startGrafana(t, iss)

	t.Run("owner is admitted as the configured role", func(t *testing.T) {
		role, r := g.orgRole(t, iss.mint(t, assertion{sub: uniqueSub(t), workspace: "acme", role: "owner"}))
		if role != "Editor" {
			t.Fatalf("role = %q (%d %s), want Editor", role, r.status, r.body)
		}
	})

	for _, tc := range []struct {
		name  string
		token func() string
	}{
		{"editor", func() string { return iss.mint(t, assertion{sub: uniqueSub(t), workspace: "acme", role: "editor"}) }},
		{"viewer", func() string { return iss.mint(t, assertion{sub: uniqueSub(t), workspace: "acme", role: "viewer"}) }},
		{"no groups claim", func() string {
			return iss.mint(t, assertion{sub: uniqueSub(t), groups: []string{}})
		}},
		{"groups claim of the wrong type", func() string {
			return iss.mint(t, assertion{sub: uniqueSub(t), groups: "/workspaces/acme/owner"})
		}},
		{"owner, wrong audience (a token minted for another module)", func() string {
			return iss.mint(t, assertion{sub: uniqueSub(t), workspace: "acme", role: "owner", aud: "logging"})
		}},
		{"owner, wrong issuer", func() string {
			return iss.mint(t, assertion{sub: uniqueSub(t), workspace: "acme", role: "owner", iss: "http://evil.example/iframe-identity"})
		}},
		{"owner, expired", func() string {
			return iss.mint(t, assertion{sub: uniqueSub(t), workspace: "acme", role: "owner", ttl: -time.Minute})
		}},
		{"owner, signed by a key core doesn't hold", func() string {
			k, _ := rsa.GenerateKey(rand.Reader, 2048)
			return iss.mint(t, assertion{sub: uniqueSub(t), workspace: "acme", role: "owner", key: k})
		}},
		{"no assertion at all (no anonymous access)", func() string { return "" }},
	} {
		t.Run("refused: "+tc.name, func(t *testing.T) {
			role, r := g.orgRole(t, tc.token())
			if role != "" || (r.status != 401 && r.status != 403) {
				t.Fatalf("admitted as %q (%d %s); want refused outright", role, r.status, r.body)
			}
		})
	}

	t.Run("losing ownership locks an admitted person out on their next request", func(t *testing.T) {
		sub := uniqueSub(t)
		if role, r := g.orgRole(t, iss.mint(t, assertion{sub: sub, workspace: "acme", role: "owner"})); role != "Editor" {
			t.Fatalf("first login: %q %d %s", role, r.status, r.body)
		}
		if role, r := g.orgRole(t, iss.mint(t, assertion{sub: sub, workspace: "acme", role: "viewer"})); role != "" {
			t.Fatalf("demoted person still admitted as %q (%d)", role, r.status)
		}
	})

	t.Run("an admitted person queries Loki through the provisioned data source", func(t *testing.T) {
		mod := lokitest.UniqueModule(t)
		lokitest.Push(t, lokiURL, map[string]string{"module": mod}, lokitest.Line{At: time.Now(), Text: "hello from " + mod})
		token := iss.mint(t, assertion{sub: uniqueSub(t), workspace: "acme", role: "owner"})
		q := map[string]any{
			"from": "now-10m", "to": "now",
			"queries": []any{map[string]any{"refId": "A", "datasource": map[string]string{"uid": "booth-loki"},
				"expr": fmt.Sprintf(`{module=%q}`, mod), "queryType": "range", "maxLines": 10}},
		}
		lokitest.Eventually(t, 20*time.Second, func() bool {
			r := g.do(t, "POST", "/api/ds/query", token, q)
			if r.status != 200 {
				t.Fatalf("ds/query: %d %s", r.status, r.body)
			}
			return bytes.Contains(r.body, []byte("hello from "+mod))
		})
	})

	t.Run("an admitted person cannot reconfigure data sources or users", func(t *testing.T) {
		token := iss.mint(t, assertion{sub: uniqueSub(t), workspace: "acme", role: "owner"})
		if r := g.do(t, "POST", "/api/datasources", token, map[string]any{"name": "x", "type": "loki", "url": "http://169.254.169.254", "access": "proxy"}); r.status != 403 {
			t.Errorf("creating a data source: %d %s, want 403", r.status, r.body)
		}
		if r := g.do(t, "DELETE", "/api/datasources/uid/booth-loki", token, nil); r.status != 403 {
			t.Errorf("deleting the Loki data source: %d %s, want 403", r.status, r.body)
		}
		if r := g.do(t, "GET", "/api/admin/settings", token, nil); r.status != 403 {
			t.Errorf("server admin settings: %d, want 403", r.status)
		}
	})

	t.Run("there is no admin account to log in with", func(t *testing.T) {
		req, _ := http.NewRequest("GET", g.base+"/api/user", nil)
		req.SetBasicAuth("admin", "admin")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode == 200 {
			t.Error("admin/admin basic auth accepted")
		}
	})
}

// With access.workspaces set, only owners acting in a listed workspace get in.
func TestAdmission_Allowlist(t *testing.T) {
	lokitest.URL(t)
	iss := newIssuer(t)
	g := startGrafana(t, iss, "--set", "access.workspaces={platform,ops}")

	for _, tc := range []struct {
		workspace, role, want string
	}{
		{"platform", "owner", "Editor"},
		{"ops", "owner", "Editor"},
		{"acme", "owner", ""},      // an owner, but of a workspace not on the list
		{"platform", "editor", ""}, // on the list, but not an owner
		{"platformx", "owner", ""}, // near-miss slug
		{"xplatform", "owner", ""},
	} {
		t.Run(tc.role+"@"+tc.workspace, func(t *testing.T) {
			role, r := g.orgRole(t, iss.mint(t, assertion{sub: uniqueSub(t), workspace: tc.workspace, role: tc.role}))
			if role != tc.want {
				t.Fatalf("role = %q (%d %s), want %q", role, r.status, r.body, tc.want)
			}
		})
	}
}

// Guards the rendered expression's shape against an accidental widening that the live tests
// above would only catch for the cases they enumerate.
func TestRoleAttributePath_Rendered(t *testing.T) {
	need(t, "helm")
	render := func(extra ...string) string {
		args := append([]string{"template", "x", chartDir, "--set", "oidc.issuerUrl=https://i", "--set", "oidc.clientId=c", "--show-only", "templates/grafana-config.yaml"}, extra...)
		out, err := exec.Command("helm", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		m := regexp.MustCompile(`role_attribute_path = (.*)`).FindSubmatch(out)
		if m == nil {
			t.Fatalf("no role_attribute_path in:\n%s", out)
		}
		return string(m[1])
	}
	if got := render("--set", "oidc.groupsClaim=memberships"); !strings.Contains(got, `"memberships"`) {
		t.Errorf("expression ignores oidc.groupsClaim: %s", got)
	}
	if got := render("--set", "access.workspaces={platform}"); strings.Contains(got, "ends_with") {
		t.Errorf("allowlisted expression must match whole group strings, not suffixes: %s", got)
	}
}
