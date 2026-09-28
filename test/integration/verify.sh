#!/usr/bin/env bash
# Layer-3 checks (contracts/testing-strategy.md) against booth-logging already installed in a
# real cluster — kind in CI (.github/workflows/integration.yml), or any cluster locally:
#
#   KUBE_CONTEXT=kind-booth-logging-dev NAMESPACE=booth-logging test/integration/verify.sh
#
# What it proves, in order:
#   1. the collector DaemonSet, Loki and the API are all ready;
#   2. the BoothModule reached the API server intact (ADR 0019);
#   3. a pod that knows nothing about logging — no SDK, no config (ADR 0022) — has its stdout
#      AND stderr collected into Loki, labelled with module/namespace/pod/container/stream,
#      with Loki's detected severity, and without duplicates;
#   4. Loki is unreachable from an unrelated pod (NetworkPolicy), while the API reaches it
#      (its /healthz is Loki's readiness);
#   5. the API refuses unauthenticated calls;
#   6. if GRAFANA_TOKENS names a directory written by test/integration/stubcore (and the chart was
#      installed with grafana.identity.issuerUrl pointing at the stub-core Service it serves —
#      see integration.yml), the Grafana view (ADR 0076): its init container fetched the stub's
#      keys, the logging-grafana BoothModule registered, an owner's signed assertion is admitted
#      as Editor and can query the real Loki, an editor's is refused outright, and Grafana is
#      unreachable from a pod that isn't booth-core's gateway.
set -euo pipefail

ctx=${KUBE_CONTEXT:+--context "$KUBE_CONTEXT"}
ns=${NAMESPACE:-booth-logging}
release=${RELEASE:-booth-logging}
probe_ns=booth-logging-it-probe
k() { kubectl $ctx "$@"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
step() { echo "--- $*"; }

step "workloads ready"
k -n "$ns" rollout status "daemonset/$release-collector" --timeout=180s
k -n "$ns" rollout status "statefulset/$release-loki" --timeout=180s
k -n "$ns" rollout status "deployment/$release" --timeout=180s

step "BoothModule registered as declared"
bm() { k -n "$ns" get boothmodules.booth.projectbooth.io logging -o jsonpath="{.spec.$1}"; }
[ "$(bm id)" = logging ] || fail "spec.id"
[ "$(bm navGroup)" = manage ] || fail "spec.navGroup"
[ "$(bm uiIntegrationMode)" = native ] || fail "spec.uiIntegrationMode"
[ "$(bm healthCheckPath)" = /healthz ] || fail "spec.healthCheckPath"

step "an unmodified pod's stdout and stderr land in Loki"
# A previous run's cleanup deletes this namespace without waiting; don't race it.
k wait --for=delete "namespace/$probe_ns" --timeout=180s >/dev/null 2>&1 || true
k create namespace "$probe_ns" --dry-run=client -o yaml | k apply -f - >/dev/null
k -n "$probe_ns" delete pod emitter --ignore-not-found --wait >/dev/null
marker="it-$(date +%s)-$RANDOM"
# Two container ports on purpose: pod discovery yields one target per port, which must not
# turn into duplicate lines.
cat <<EOF | k apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: emitter
  namespace: $probe_ns
  labels:
    app.kubernetes.io/name: booth-itprobe
spec:
  restartPolicy: Never
  containers:
    - name: app
      image: busybox:1.36
      ports: [{containerPort: 80}, {containerPort: 81}]
      command: ["sh", "-c", "echo '{\"level\":\"error\",\"msg\":\"$marker json\"}'; echo 'level=warn msg=$marker-stderr' >&2; echo '$marker plain'; sleep 3600"]
EOF
k -n "$probe_ns" wait --for=condition=Ready pod/emitter --timeout=120s >/dev/null

# Query Loki through a port-forward to its Service: the NetworkPolicy (rightly) keeps other
# pods out, and port-forwarded traffic enters the pod directly.
lport=${LOKI_LOCAL_PORT:-13100}
pflog=$(mktemp)
k -n "$ns" port-forward "svc/$release-loki" "$lport:3100" >"$pflog" 2>&1 &
pf=$!
trap 'kill $pf 2>/dev/null || true; k delete namespace "$probe_ns" --wait=false >/dev/null 2>&1 || true' EXIT
for i in $(seq 1 30); do
  [ "$(curl -s "http://127.0.0.1:$lport/ready" || true)" = ready ] && break
  [ "$i" = 30 ] && fail "Loki never answered through the port-forward: $(cat "$pflog")"
  sleep 1
done

query() {
  curl -sSfG "http://127.0.0.1:$lport/loki/api/v1/query_range" \
    --data-urlencode "query={module=\"itprobe\"} |= \"$marker\"" \
    --data-urlencode "start=$(( $(date +%s) - 600 ))000000000" --data-urlencode limit=100
}
got=""
for _ in $(seq 1 45); do
  got=$(query) || { echo "query failed; retrying" >&2; got='{}'; }
  n=$(echo "$got" | jq '[.data.result[]?.values[]] | length')
  [ "$n" -ge 3 ] && break
  sleep 2
done
echo "$got" | jq -c '.data.result[] | {stream: .stream, lines: [.values[][1]]}'
n=$(echo "$got" | jq '[.data.result[].values[]] | length')
[ "$n" -eq 3 ] || fail "want exactly 3 lines for marker $marker (no duplicates), got $n"

line_label() { echo "$got" | jq -r --arg s "$1" --arg l "$2" '.data.result[] | select(any(.values[]; .[1] | contains($s))) | .stream[$l]'; }
[ "$(line_label json namespace)" = "$probe_ns" ] || fail "namespace label"
[ "$(line_label json pod)" = emitter ] || fail "pod label"
[ "$(line_label json container)" = app ] || fail "container label"
[ "$(line_label json module)" = itprobe ] || fail "module label (booth- prefix stripped)"
[ "$(line_label json stream)" = stdout ] || fail "stdout stream"
[ "$(line_label json detected_level)" = error ] || fail "JSON level detected"
[ "$(line_label -stderr stream)" = stderr ] || fail "stderr collected as stream=stderr"
[ "$(line_label -stderr detected_level)" = warn ] || fail "logfmt level detected"
# The CRI prefix (timestamp stream flag) must be stripped: Loki stores what the process wrote.
[ "$(echo "$got" | jq -r '.data.result[].values[][1]' | grep -c "^$marker plain\$")" -eq 1 ] || fail "CRI format not unwrapped"

step "the chart's own pods are filed under module=logging"
curl -sfG "http://127.0.0.1:$lport/loki/api/v1/label/module/values" | jq -e '.data | index("logging")' >/dev/null || fail "module=logging missing"

step "Loki is closed to other pods; the API reaches it"
out=$(k -n "$probe_ns" run np-probe --rm -i --restart=Never --image=curlimages/curl -- \
  sh -c "curl -s -m 5 -o /dev/null -w 'status=%{http_code}' http://$release-loki.$ns:3100/ready; echo \" exit=\$?\"" 2>/dev/null || true)
echo "$out" | grep -q "status=000" || fail "an unrelated pod reached Loki: $out"
out=$(k -n "$probe_ns" run hz-probe --rm -i --restart=Never --image=curlimages/curl -- \
  curl -s -w ' status=%{http_code}' "http://$release.$ns:8080/healthz" 2>/dev/null || true)
echo "$out" | grep -q 'status=200' || fail "API /healthz: $out"

step "the API refuses unauthenticated requests"
out=$(k -n "$probe_ns" run unauth-probe --rm -i --restart=Never --image=curlimages/curl -- \
  curl -s -o /dev/null -w 'status=%{http_code}' "http://$release.$ns:8080/api/logs" 2>/dev/null || true)
echo "$out" | grep -q 'status=401' || fail "unauthenticated /api/logs: $out"

if [ -n "${GRAFANA_TOKENS:-}" ]; then
  step "Grafana view: registered, keys fetched by the init container, ready"
  k -n "$ns" rollout status "deployment/$release-grafana" --timeout=180s
  gm() { k -n "$ns" get boothmodules.booth.projectbooth.io logging-grafana -o jsonpath="{.spec.$1}"; }
  [ "$(gm id)" = logging-grafana ] || fail "logging-grafana spec.id"
  [ "$(gm uiIntegrationMode)" = iframe-proxy ] || fail "logging-grafana uiIntegrationMode"
  [ "$(gm navPath)" = /logging-grafana ] || fail "logging-grafana navPath"
  [ "$(bm uiIntegrationMode)" = native ] || fail "the native registration changed"

  owner=$(cat "$GRAFANA_TOKENS/owner.jwt")
  editor=$(cat "$GRAFANA_TOKENS/editor.jwt")
  g="http://$release-grafana.$ns:3000"
  # The Grafana NetworkPolicy admits only booth-core's pods; these probes wear its label.
  as_core() {
    k -n "$probe_ns" run "$1" --rm -i --restart=Never --image=curlimages/curl       --labels=app.kubernetes.io/name=booth-core -- sh -c "$2" 2>/dev/null || true
  }

  step "Grafana admits an owner as Editor"
  out=$(as_core g-owner "curl -s -H 'X-Booth-Identity: $owner' $g/api/user/orgs")
  echo "$out" | grep -q '"role":"Editor"' || fail "owner not admitted as Editor: $out"

  step "Grafana refuses an editor outright"
  out=$(as_core g-editor "curl -s -o /dev/null -w 'status=%{http_code}' -H 'X-Booth-Identity: $editor' $g/api/user/orgs")
  echo "$out" | grep -qE 'status=40[13]' || fail "editor not refused: $out"

  step "an admitted owner queries the real Loki through Grafana"
  body='{"from":"now-15m","to":"now","queries":[{"refId":"A","datasource":{"uid":"booth-loki"},"expr":"{module=\"itprobe\"} |= \"'"$marker"'\"","queryType":"range","maxLines":10}]}'
  out=$(as_core g-query "curl -s -H 'X-Booth-Identity: $owner' -H 'Content-Type: application/json' -H 'Origin: $g' -d '$body' $g/api/ds/query")
  echo "$out" | grep -q "$marker plain" || fail "Grafana query didn't return the probe's lines: $(echo "$out" | head -c 400)"

  step "Grafana is closed to pods other than booth-core's"
  out=$(k -n "$probe_ns" run g-stranger --rm -i --restart=Never --image=curlimages/curl --     sh -c "curl -s -m 5 -o /dev/null -w 'status=%{http_code}' $g/api/health; echo" 2>/dev/null || true)
  echo "$out" | grep -q "status=000" || fail "an unrelated pod reached Grafana: $out"
fi

echo "PASS"
