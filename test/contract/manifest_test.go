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

// ---- the Grafana view (ADR 0076, as amended by ADR 0077) ----------------------------------------

// withOps configures an operator workspace: the Grafana view exists only when there is one
// (ADR 0077 admits operators only).
var withOps = []string{"--set", "access.workspaces={platform}"}

func ops(extra ...string) []string { return append(append([]string{}, withOps...), extra...) }

func renderGrafanaModule(t *testing.T, extra ...string) (boothModule, bool) {
	t.Helper()
	for _, d := range docs(t, helmTemplate(t, "", extra...)) {
		if d["kind"] != "BoothModule" {
			continue
		}
		if spec, _ := d["spec"].(map[string]any); spec["id"] == "logging-grafana" {
			raw, _ := yaml.Marshal(d)
			var m boothModule
			if err := yaml.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			return m, true
		}
	}
	return boothModule{}, false
}

// A second registration from this chart (module-manifest.md's multi-surface pattern), beside
// the unchanged `logging` one.
func TestGrafana_SecondRegistration(t *testing.T) {
	m, ok := renderGrafanaModule(t, withOps...)
	if !ok {
		t.Fatal("no logging-grafana BoothModule rendered")
	}
	if m.Spec.UIIntegrationMode != "iframe-proxy" || m.Spec.NavGroup != "manage" || m.Spec.NavPath != "/logging-grafana" || !m.Spec.HasOwnUI {
		t.Errorf("spec = %+v", m.Spec)
	}
	if m.Spec.ServiceRef.Name != "booth-logging-grafana" || m.Spec.ServiceRef.Port != 3000 {
		t.Errorf("serviceRef = %+v, want the Grafana Service", m.Spec.ServiceRef)
	}
	if !semver.MatchString(m.Spec.Version) || !semver.MatchString(m.Spec.ContractVersion) || m.Spec.DisplayName == "" {
		t.Errorf("required fields: %+v", m.Spec)
	}
	if m.Spec.Events != nil || m.Spec.Database != nil || m.Spec.WorkloadIdentity != nil {
		t.Error("the Grafana registration declares capabilities it doesn't use")
	}
	dep := helmTemplate(t, "templates/grafana.yaml", withOps...)
	if !regexp.MustCompile(`readinessProbe:\s+httpGet:\s+path: ` + regexp.QuoteMeta(m.Spec.HealthCheckPath) + `\b`).Match(dep) {
		t.Errorf("Grafana readinessProbe doesn't use healthCheckPath %q", m.Spec.HealthCheckPath)
	}
	// The native viewer's registration is untouched.
	if n := renderBoothModule(t); n.Spec.ID != "logging" || n.Spec.UIIntegrationMode != "native" {
		t.Errorf("native registration changed: %+v", n.Spec)
	}
}

func grafanaINI(t *testing.T, extra ...string) string {
	t.Helper()
	var cm struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(helmTemplate(t, "templates/grafana-config.yaml", ops(extra...)...), &cm); err != nil {
		t.Fatal(err)
	}
	return cm.Data["grafana.ini"]
}

// Every way into Grafana other than core's signed assertion is off, and the gate is strict.
// test/grafana proves these settings behave as intended in a real Grafana; this pins them.
func TestGrafana_Hardening(t *testing.T) {
	ini := grafanaINI(t)
	for _, want := range []string{
		"disable_initial_admin_creation = true",
		"disable_login_form = true",
		"[auth.basic]\nenabled = false",
		"[auth.anonymous]\nenabled = false",
		"header_name = X-Booth-Identity",
		`"aud": ["logging-grafana"]`,
		"role_attribute_strict = true",
		"skip_org_role_sync = false",
		"allow_assign_grafana_admin = false",
		"allow_embedding = true",
		"root_url = %(protocol)s://%(domain)s/iframe/logging-grafana/",
		"serve_from_sub_path = false",
	} {
		if !strings.Contains(ini, want) {
			t.Errorf("grafana.ini missing %q", want)
		}
	}
	if regexp.MustCompile(`(?m)^jwk_set_url\s*=`).MatchString(ini) {
		t.Error("jwk_set_url set: Grafana refuses a non-https JWKS URL, core's issuer is http (see internal/jwksfetch)")
	}
}

// The init container fetches keys for exactly the issuer Grafana expects in `iss`, and the
// data source points at this release's Loki, read-only.
func TestGrafana_IssuerAndDataSourceWiring(t *testing.T) {
	const iss = "http://core.example.svc.cluster.local:8080/iframe-identity"
	ini := grafanaINI(t, "--set", "grafana.identity.issuerUrl="+iss+"/")
	if !strings.Contains(ini, `"iss": "`+iss+`"`) {
		t.Errorf("expect_claims iss not %q (trailing slash must be trimmed):\n%s", iss, ini)
	}
	dep := string(helmTemplate(t, "templates/grafana.yaml", ops("--set", "grafana.identity.issuerUrl="+iss+"/")...))
	if !strings.Contains(dep, "- -issuer="+iss+"\n") || !strings.Contains(dep, "- fetch-jwks") {
		t.Errorf("init container not fetching keys for %q:\n%s", iss, dep)
	}
	var cm struct {
		Data map[string]string `yaml:"data"`
	}
	_ = yaml.Unmarshal(helmTemplate(t, "templates/grafana-config.yaml", withOps...), &cm)
	ds := cm.Data["datasources.yaml"]
	if !strings.Contains(ds, "url: http://booth-logging-loki:3100") || !strings.Contains(ds, "editable: false") {
		t.Errorf("datasources.yaml:\n%s", ds)
	}
}

func TestGrafana_Validation(t *testing.T) {
	needHelm(t)
	for _, tc := range []struct{ set, want string }{
		{"grafana.admittedRole=Admin", "grafana.admittedRole must be Viewer or Editor"},
		{"access.workspaces={Platform_1}", "access.workspaces entries must be workspace slugs"},
		{"access.workspaces={a'b}", "access.workspaces entries must be workspace slugs"},
		{"grafana.identity.issuerUrl=", "grafana.identity.issuerUrl is required"},
	} {
		// The case's own --set goes last, so it wins over withOps.
		args := append(append([]string{"template", "x", chartDir}, ops(requiredValues...)...), "--set", tc.set)
		out, err := exec.Command("helm", args...).CombinedOutput()
		if err == nil || !bytes.Contains(out, []byte(tc.want)) {
			t.Errorf("%s: want failure %q, got: %s", tc.set, tc.want, out)
		}
	}
}

// Loki admits Grafana; Grafana admits only core's gateway (defense in depth, ADR 0076).
func TestGrafana_NetworkPolicies(t *testing.T) {
	var loki, graf map[string]any
	for _, d := range docs(t, helmTemplate(t, "templates/networkpolicy.yaml", withOps...)) {
		switch d["metadata"].(map[string]any)["name"] {
		case "booth-logging-loki":
			loki = d
		case "booth-logging-grafana":
			graf = d
		}
	}
	if loki == nil || graf == nil {
		t.Fatal("want both NetworkPolicies")
	}
	lk, _ := yaml.Marshal(loki)
	if !strings.Contains(string(lk), "app.kubernetes.io/component: grafana") {
		t.Errorf("Loki's NetworkPolicy doesn't admit Grafana:\n%s", lk)
	}
	gf, _ := yaml.Marshal(graf)
	for _, want := range []string{"app.kubernetes.io/name: booth-core", "port: 3000", "app.kubernetes.io/component: grafana"} {
		if !strings.Contains(string(gf), want) {
			t.Errorf("Grafana NetworkPolicy missing %q:\n%s", want, gf)
		}
	}
}

// No Grafana at all when it's disabled — or when there are no operators to admit (ADR 0077):
// no workload, no registration (so no nav entry that refuses everyone), no NetworkPolicy, and
// Loki's policy doesn't admit it.
func TestGrafana_AbsentWithoutOperatorsOrWhenDisabled(t *testing.T) {
	for name, extra := range map[string][]string{
		"disabled":                   ops("--set", "grafana.enabled=false"),
		"no operators (the default)": nil,
		"explicitly no operators":    {"--set", "access.workspaces=null"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := renderGrafanaModule(t, extra...); ok {
				t.Error("logging-grafana registered")
			}
			all := string(helmTemplate(t, "", extra...))
			for _, gone := range []string{"booth-logging-grafana", "app.kubernetes.io/component: grafana"} {
				if strings.Contains(all, gone) {
					t.Errorf("%q still rendered", gone)
				}
			}
			if m := renderBoothModule(t); m.Spec.ID != "logging" {
				t.Errorf("native registration missing: %+v", m.Spec)
			}
		})
	}
}

// ADR 0077: Grafana admits operators only — the expression lists exactly the operator
// workspaces' owner groups, whole-string, and has no "any owner" form.
func TestGrafana_AdmitsOperatorsOnly(t *testing.T) {
	ini := grafanaINI(t, "--set", "access.workspaces={platform,ops}")
	m := regexp.MustCompile(`role_attribute_path = (.*)`).FindStringSubmatch(ini)
	if m == nil {
		t.Fatal("no role_attribute_path")
	}
	want := "(contains((\"groups\" || `[]`), '/workspaces/platform/owner') || contains((\"groups\" || `[]`), '/workspaces/ops/owner')) && 'Editor' || ''"
	if m[1] != want {
		t.Errorf("role_attribute_path =\n %s\nwant\n %s", m[1], want)
	}
}

// ADR 0077: the workspace label comes from the pod's booth.projectbooth.io/workspace label and
// nothing else. In particular no stage of the line-processing pipeline may turn line content
// into labels — otherwise a pod could label its own lines into another tenant's view.
func TestCollector_WorkspaceLabelFromPodMetadataOnly(t *testing.T) {
	cfg := string(helmTemplate(t, "templates/collector-config.yaml"))
	rule := regexp.MustCompile(`rule \{\s+source_labels = \["__meta_kubernetes_pod_label_booth_projectbooth_io_workspace"\]\s+regex\s+= "\(\[a-z0-9-\]\+\)"\s+target_label\s+= "workspace"\s+\}`)
	if !rule.MatchString(cfg) {
		t.Errorf("no workspace rule copying the pod label (slug-validated):\n%s", cfg)
	}
	if n := strings.Count(cfg, `target_label  = "workspace"`); n != 1 {
		t.Errorf("%d rules set the workspace label; want exactly the one from the pod label", n)
	}

	// Nothing in loki.process may extract labels or structured metadata from a line.
	i := strings.Index(cfg, `loki.process "pods"`)
	j := strings.Index(cfg, `loki.write "loki"`)
	if i < 0 || j < i {
		t.Fatal("loki.process block not found")
	}
	var stages []string
	for _, m := range regexp.MustCompile(`(?m)^\s*(stage\.[a-z_]+)`).FindAllStringSubmatch(cfg[i:j], -1) {
		stages = append(stages, m[1])
	}
	if strings.Join(stages, ",") != "stage.cri,stage.label_drop" {
		t.Errorf("loki.process stages = %v; only stage.cri and stage.label_drop are allowed (no stage may turn line content into labels)", stages)
	}
}
