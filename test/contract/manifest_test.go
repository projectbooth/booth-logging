// Package contract validates booth-logging's manifest — the BoothModule custom resource its
// Helm chart templates — against contracts/module-manifest.md, and pins the chart
// properties the rest of this module depends on (retention wiring, the Loki config the Go
// tests ran against, the collector's reach). Per contracts/testing-strategy.md this runs
// against `helm template` output, not a cluster: no cluster needed, but a `helm` binary is.
package contract

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var requiredValues = []string{
	"--set", "oidc.issuerUrl=https://keycloak.example.com/realms/booth",
	"--set", "oidc.clientId=booth-logging",
}

var chartDir = filepath.Join("..", "..", "charts", "booth-logging")

func needHelm(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		if os.Getenv("BOOTH_TEST_STRICT") == "1" {
			t.Fatal("helm not installed")
		}
		t.Skip("helm not installed; this contract test runs in CI where it is (see .github/workflows/ci.yml)")
	}
}

func helmTemplate(t *testing.T, showOnly string, extra ...string) []byte {
	t.Helper()
	needHelm(t)
	args := []string{"template", "booth-logging", chartDir, "--namespace", "booth-logging"}
	args = append(args, requiredValues...)
	args = append(args, extra...)
	if showOnly != "" {
		args = append(args, "--show-only", showOnly)
	}
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	return out
}

// docs splits a multi-document render into generic maps.
func docs(t *testing.T, out []byte) []map[string]any {
	t.Helper()
	dec := yaml.NewDecoder(bytes.NewReader(out))
	var res []map[string]any
	for {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			if err == io.EOF {
				return res
			}
			t.Fatalf("parsing rendered yaml: %v", err)
		}
		if m != nil {
			res = append(res, m)
		}
	}
}

type boothModule struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Spec       struct {
		ID                string         `yaml:"id"`
		DisplayName       string         `yaml:"displayName"`
		Version           string         `yaml:"version"`
		ContractVersion   string         `yaml:"contractVersion"`
		HasOwnUI          bool           `yaml:"hasOwnUi"`
		UIIntegrationMode string         `yaml:"uiIntegrationMode"`
		HealthCheckPath   string         `yaml:"healthCheckPath"`
		NavGroup          string         `yaml:"navGroup"`
		NavPath           string         `yaml:"navPath"`
		AdminNavPath      string         `yaml:"adminNavPath"`
		Events            map[string]any `yaml:"events"`
		Database          map[string]any `yaml:"database"`
		WorkloadIdentity  map[string]any `yaml:"workloadIdentity"`
		ServiceRef        struct {
			Name string `yaml:"name"`
			Port int    `yaml:"port"`
		} `yaml:"serviceRef"`
	} `yaml:"spec"`
}

func renderBoothModule(t *testing.T) boothModule {
	t.Helper()
	var m boothModule
	if err := yaml.Unmarshal(helmTemplate(t, "templates/boothmodule.yaml"), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

var semver = regexp.MustCompile(`^\d+\.\d+\.\d+`)

func TestManifest_RequiredFields(t *testing.T) {
	m := renderBoothModule(t)
	if m.APIVersion != "booth.projectbooth.io/v1alpha1" || m.Kind != "BoothModule" {
		t.Errorf("apiVersion/kind = %s/%s (ADR 0019)", m.APIVersion, m.Kind)
	}
	if m.Spec.ID != "logging" {
		t.Errorf("spec.id = %q, want logging (repo name minus booth-)", m.Spec.ID)
	}
	if m.Spec.DisplayName == "" || !semver.MatchString(m.Spec.Version) || !semver.MatchString(m.Spec.ContractVersion) {
		t.Errorf("displayName/version/contractVersion invalid: %+v", m.Spec)
	}
	if !strings.HasPrefix(m.Spec.HealthCheckPath, "/") {
		t.Errorf("healthCheckPath = %q", m.Spec.HealthCheckPath)
	}
	if m.Spec.ServiceRef.Name != "booth-logging" || m.Spec.ServiceRef.Port != 8080 {
		t.Errorf("serviceRef = %+v, want the API Service", m.Spec.ServiceRef)
	}
}

func TestManifest_UI(t *testing.T) {
	m := renderBoothModule(t)
	if !m.Spec.HasOwnUI || m.Spec.UIIntegrationMode != "native" {
		t.Errorf("hasOwnUi/uiIntegrationMode = %v/%q, want true/native (ui-integration.md: the basic viewer is native)", m.Spec.HasOwnUI, m.Spec.UIIntegrationMode)
	}
	if m.Spec.NavGroup != "manage" {
		t.Errorf("navGroup = %q, want manage (brief; ADR 0017)", m.Spec.NavGroup)
	}
	if m.Spec.NavPath != "/logging" {
		t.Errorf("navPath = %q", m.Spec.NavPath)
	}
}

// ADR 0022: no ingestion API, no bus, no database. Anything declared here would get the
// module credentials it has no use for.
func TestManifest_DeclaresNothingItDoesntUse(t *testing.T) {
	m := renderBoothModule(t)
	if m.Spec.Events != nil || m.Spec.Database != nil || m.Spec.WorkloadIdentity != nil || m.Spec.AdminNavPath != "" {
		t.Errorf("unexpected optional fields: events=%v database=%v workloadIdentity=%v adminNavPath=%q",
			m.Spec.Events, m.Spec.Database, m.Spec.WorkloadIdentity, m.Spec.AdminNavPath)
	}
}

func TestManifest_HealthPathMatchesReadinessProbe(t *testing.T) {
	m := renderBoothModule(t)
	dep := helmTemplate(t, "templates/deployment.yaml")
	if !regexp.MustCompile(`readinessProbe:\s+httpGet:\s+path: ` + regexp.QuoteMeta(m.Spec.HealthCheckPath) + `\b`).Match(dep) {
		t.Errorf("readinessProbe does not use healthCheckPath %q", m.Spec.HealthCheckPath)
	}
	if regexp.MustCompile(`livenessProbe:\s+httpGet:\s+path: /healthz`).Match(dep) {
		t.Error("livenessProbe uses the Loki-aware health path; restarting the API can't fix Loki")
	}
}

// lokiConfig returns the rendered Loki config.yaml, parsed.
func lokiConfig(t *testing.T, extra ...string) map[string]any {
	t.Helper()
	var cm struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(helmTemplate(t, "templates/loki-config.yaml", extra...), &cm); err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(cm.Data["config.yaml"]), &cfg); err != nil {
		t.Fatalf("rendered Loki config is not YAML: %v\n%s", err, cm.Data["config.yaml"])
	}
	return cfg
}

// The Go tests' real-Loki layer runs against hack/loki-local.yaml. If the chart's Loki
// config drifted from it, those tests would be proving things about a different Loki.
func TestLokiConfig_MatchesTestedConfig(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "hack", "loki-local.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var tested map[string]any
	if err := yaml.Unmarshal(raw, &tested); err != nil {
		t.Fatal(err)
	}
	if got := lokiConfig(t); !reflect.DeepEqual(got, tested) {
		gotY, _ := yaml.Marshal(got)
		t.Errorf("chart's Loki config (default values) differs from hack/loki-local.yaml:\n--- chart\n%s--- hack\n%s", gotY, raw)
	}
}

// The ruling: retention is short by default and a configurable Loki setting. Pins the
// default (14 days), that one value drives every place it matters, and that retention is
// actually enforced (the compactor ignores retention_period unless retention_enabled).
func TestRetention(t *testing.T) {
	check := func(t *testing.T, cfg map[string]any, wantHours string) {
		t.Helper()
		limits := cfg["limits_config"].(map[string]any)
		for _, k := range []string{"retention_period", "max_query_lookback", "reject_old_samples_max_age"} {
			if limits[k] != wantHours {
				t.Errorf("limits_config.%s = %v, want %s", k, limits[k], wantHours)
			}
		}
		if c := cfg["compactor"].(map[string]any); c["retention_enabled"] != true {
			t.Error("compactor.retention_enabled must be true, or retention is never enforced")
		}
	}
	t.Run("default 14 days", func(t *testing.T) {
		check(t, lokiConfig(t), "336h")
		if !regexp.MustCompile(`BOOTH_LOGGING_RETENTION\s+value: "336h"`).Match(helmTemplate(t, "templates/deployment.yaml")) {
			t.Error("API not told the same retention as Loki")
		}
	})
	t.Run("configurable", func(t *testing.T) {
		check(t, lokiConfig(t, "--set", "loki.retentionDays=7"), "168h")
		if !regexp.MustCompile(`BOOTH_LOGGING_RETENTION\s+value: "168h"`).Match(helmTemplate(t, "templates/deployment.yaml", "--set", "loki.retentionDays=7")) {
			t.Error("API retention doesn't follow loki.retentionDays")
		}
	})
	t.Run("invalid values fail loudly", func(t *testing.T) {
		needHelm(t)
		for _, v := range []string{"0", "1.5", "-3", "two"} {
			args := append([]string{"template", "x", chartDir, "--set", "loki.retentionDays=" + v}, requiredValues...)
			out, err := exec.Command("helm", args...).CombinedOutput()
			if err == nil || !bytes.Contains(out, []byte("loki.retentionDays must be a whole number")) {
				t.Errorf("retentionDays=%s: rendered or failed unhelpfully: %s", v, out)
			}
		}
	})
}

// ADR 0022: a node-level collector on every node, tailing container logs read-only.
func TestCollector(t *testing.T) {
	var ds, role map[string]any
	for _, d := range docs(t, helmTemplate(t, "templates/collector.yaml")) {
		switch d["kind"] {
		case "DaemonSet":
			ds = d
		case "ClusterRole":
			role = d
		case "Role":
			t.Error("unexpected namespaced Role")
		}
	}
	if ds == nil {
		t.Fatal("no DaemonSet rendered")
	}
	spec := ds["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	tol, _ := yaml.Marshal(spec["tolerations"])
	if !strings.Contains(string(tol), "operator: Exists") {
		t.Errorf("collector must tolerate every taint so no node's pods go uncollected: %s", tol)
	}
	c := spec["containers"].([]any)[0].(map[string]any)
	mounts, _ := yaml.Marshal(c["volumeMounts"])
	if !regexp.MustCompile(`mountPath: /var/log/pods\s+name: pod-logs\s+readOnly: true`).Match(mounts) {
		t.Errorf("/var/log/pods must be mounted read-only: %s", mounts)
	}
	sc, _ := yaml.Marshal(c["securityContext"])
	for _, want := range []string{"allowPrivilegeEscalation: false", "readOnlyRootFilesystem: true", "- ALL"} {
		if !strings.Contains(string(sc), want) {
			t.Errorf("collector securityContext missing %q: %s", want, sc)
		}
	}

	// Cluster-wide read on pod metadata, and nothing else.
	rules, _ := yaml.Marshal(role["rules"])
	want := "- apiGroups:\n    - \"\"\n  resources:\n    - pods\n  verbs:\n    - get\n    - list\n    - watch\n"
	if string(rules) != want {
		t.Errorf("collector ClusterRole rules:\n%s\nwant exactly:\n%s", rules, want)
	}
}

func TestCollector_ConfigShipsToThisReleasesLoki(t *testing.T) {
	cfg := string(helmTemplate(t, "templates/collector-config.yaml"))
	for _, want := range []string{
		`url = "http://booth-logging-loki:3100/loki/api/v1/push"`,
		`field = "spec.nodeName=" + sys.env("NODE_NAME")`,
		`stage.cri {}`,
		`target_label  = "module"`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("collector config missing %q", want)
		}
	}
	if strings.Contains(cfg, `action        = "drop"`) {
		t.Error("no namespace should be excluded by default (ADR 0022: every pod)")
	}
	ex := string(helmTemplate(t, "templates/collector-config.yaml", "--set", "collector.excludeNamespaces={kube-system,noisy}"))
	if !strings.Contains(ex, `regex         = "kube-system|noisy"`) {
		t.Errorf("excludeNamespaces not rendered:\n%s", ex)
	}
}

// Loki has no auth; the NetworkPolicy is its access control.
func TestNetworkPolicy(t *testing.T) {
	out := string(helmTemplate(t, "templates/networkpolicy.yaml"))
	for _, want := range []string{"app.kubernetes.io/component: loki", "app.kubernetes.io/component: collector", "app.kubernetes.io/component: api", "port: 3100"} {
		if !strings.Contains(out, want) {
			t.Errorf("NetworkPolicy missing %q:\n%s", want, out)
		}
	}
	if n := len(docs(t, helmTemplate(t, "", "--set", "networkPolicy.enabled=false"))); n == 0 {
		t.Fatal("render with networkPolicy disabled failed")
	}
	if strings.Contains(string(helmTemplate(t, "", "--set", "networkPolicy.enabled=false")), "kind: NetworkPolicy") {
		t.Error("networkPolicy.enabled=false still renders a NetworkPolicy")
	}
}

func TestChart_AccessAndAuthEnv(t *testing.T) {
	dep := helmTemplate(t, "templates/deployment.yaml")
	if bytes.Contains(dep, []byte("BOOTH_LOGGING_ACCESS_WORKSPACES")) || bytes.Contains(dep, []byte("BOOTH_WORKLOAD_ISSUER_URL")) {
		t.Error("optional env rendered by default")
	}
	if !regexp.MustCompile(`BOOTH_OIDC_GROUPS_CLAIM\s+value: "groups"`).Match(dep) {
		t.Error("groups claim should default to booth-core's \"groups\"")
	}
	dep = helmTemplate(t, "templates/deployment.yaml", "--set", "access.workspaces={platform,ops}", "--set", "queryMaxRange=24h")
	if !regexp.MustCompile(`BOOTH_LOGGING_ACCESS_WORKSPACES\s+value: "platform,ops"`).Match(dep) {
		t.Errorf("access.workspaces not rendered:\n%s", dep)
	}
	if !regexp.MustCompile(`BOOTH_LOGGING_MAX_QUERY_RANGE\s+value: "24h"`).Match(dep) {
		t.Error("queryMaxRange not rendered")
	}

	needHelm(t)
	out, err := exec.Command("helm", "template", "x", chartDir).CombinedOutput()
	if err == nil || !bytes.Contains(out, []byte("oidc.issuerUrl is required")) {
		t.Errorf("rendering without OIDC settings should fail loudly: %s", out)
	}
}
