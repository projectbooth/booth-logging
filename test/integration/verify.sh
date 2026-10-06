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
#   5a. (ADR 0077) a pod labelled booth.projectbooth.io/workspace gets a matching workspace label
#      on its lines, and a pod WITHOUT the label gets none — even though its output claims a
#      workspace in JSON and logfmt: labels come from pod metadata, never from log content;
#   6. if GRAFANA_TOKENS names a directory written by test/integration/stubcore (and the chart was
#      installed with grafana.identity.issuerUrl pointing at the stub-core Service it serves —
#      see integration.yml), the Grafana view (ADR 0076; operators only, ADR 0077; operators hold
#      /platform/operator, ADR 0094): its init container fetched the stub's keys, the
#      logging-grafana BoothModule registered, a platform operator's signed assertion is admitted
#      as Editor and can query the real Loki, an owner without the claim (what core mints today)
#      and an editor are refused outright, and Grafana is unreachable from a pod that isn't
#      booth-core's gateway.
set -euo pipefail

ctx=${KUBE_CONTEXT:+--context "$KUBE_CONTEXT"}
ns=${NAMESPACE:-booth-logging}
release=${RELEASE:-booth-logging}
probe_ns=booth-logging-it-probe
k() { kubectl $ctx "$@"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
step() { echo "--- $*"; }

# probe NAME LABELS COMMAND — run COMMAND (sh -c) in a one-off curl pod, wait for it to finish,
# and print its output. Not `kubectl run --rm -i`: when the container exits before kubectl
# attaches, that loses the output (the attach warning goes to stderr) and a passing check reads
# as an empty failure — which is what failed Integration run 37541373382 at "API /healthz" while
# the API and Loki were both healthy. Waiting for the pod's phase and then reading its logs
# can't race.
probe() {
  local name=$1 labels=$2 cmd=$3 phase=""
  k -n "$probe_ns" delete pod "$name" --ignore-not-found --wait >/dev/null 2>&1
  k -n "$probe_ns" run "$name" --restart=Never --image=curlimages/curl ${labels:+--labels="$labels"} \
    --command -- sh -c "$cmd" >/dev/null
  for _ in $(seq 1 120); do
    phase=$(k -n "$probe_ns" get pod "$name" -o jsonpath='{.status.phase}' 2>/dev/null)
    case "$phase" in Succeeded|Failed) break ;; esac
    sleep 1
  done
  k -n "$probe_ns" logs "pod/$name" 2>&1 || echo "(no logs; pod phase: ${phase:-unknown})"
  k -n "$probe_ns" delete pod "$name" --wait=false >/dev/null 2>&1 || true
}

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
k -n "$probe_ns" delete pod emitter ws-emitter --ignore-not-found --wait >/dev/null
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
      command: ["sh", "-c", "echo '{\"level\":\"error\",\"workspace\":\"acme\",\"msg\":\"$marker json\"}'; echo 'level=warn workspace=acme msg=$marker-stderr' >&2; echo '$marker plain'; sleep 3600"]
---
# ADR 0077: a pod belonging to one workspace, labelled the way booth-notebooks' spawner does it.
apiVersion: v1
kind: Pod
metadata:
  name: ws-emitter
  namespace: $probe_ns
  labels:
    app.kubernetes.io/name: booth-itprobe
    booth.projectbooth.io/workspace: acme
spec:
  restartPolicy: Never
  containers:
    - name: app
      image: busybox:1.36
      command: ["sh", "-c", "echo '$marker from-acme-pod'; sleep 3600"]
EOF
k -n "$probe_ns" wait --for=condition=Ready pod/emitter pod/ws-emitter --timeout=120s >/dev/null

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
    --data-urlencode "query={module=\"itprobe\", pod=\"${1:-emitter}\"} |= \"$marker\"" \
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

step "workspace label: from the pod's metadata only, never from what it logged (ADR 0077)"
[ "$(line_label json workspace)" = null ] || fail "an unlabelled pod's JSON 'workspace' field became a label"
[ "$(line_label -stderr workspace)" = null ] || fail "an unlabelled pod's logfmt 'workspace=' became a label"
got=""
for _ in $(seq 1 45); do
  got=$(query ws-emitter) || { echo "query failed; retrying" >&2; got='{}'; }
  [ "$(echo "$got" | jq '[.data.result[]?.values[]] | length')" -ge 1 ] && break
  sleep 2
done
[ "$(line_label from-acme-pod workspace)" = acme ] || fail "labelled pod's lines lack workspace=acme: $(echo "$got" | head -c 300)"

step "the chart's own pods are filed under module=logging"
curl -sfG "http://127.0.0.1:$lport/loki/api/v1/label/module/values" | jq -e '.data | index("logging")' >/dev/null || fail "module=logging missing"

step "Loki is closed to other pods; the API reaches it"
out=$(probe np-probe "" "curl -s -m 5 -o /dev/null -w 'status=%{http_code}' http://$release-loki.$ns:3100/ready; echo \" exit=\$?\"")
echo "$out" | grep -q "status=000" || fail "an unrelated pod reached Loki: $out"
out=$(probe hz-probe "" "curl -sS -m 10 -w ' status=%{http_code}' http://$release.$ns:8080/healthz")
echo "$out" | grep -q 'status=200' || fail "API /healthz: $out"

step "the API refuses unauthenticated requests"
out=$(probe unauth-probe "" "curl -sS -m 10 -o /dev/null -w 'status=%{http_code}' http://$release.$ns:8080/api/logs")
echo "$out" | grep -q 'status=401' || fail "unauthenticated /api/logs: $out"

if [ -n "${GRAFANA_TOKENS:-}" ]; then
  step "Grafana view: registered, keys fetched by the init container, ready"
  k -n "$ns" rollout status "deployment/$release-grafana" --timeout=180s
  gm() { k -n "$ns" get boothmodules.booth.projectbooth.io logging-grafana -o jsonpath="{.spec.$1}"; }
  [ "$(gm id)" = logging-grafana ] || fail "logging-grafana spec.id"
  [ "$(gm uiIntegrationMode)" = iframe-proxy ] || fail "logging-grafana uiIntegrationMode"
  [ "$(gm navPath)" = /logging-grafana ] || fail "logging-grafana navPath"
  [ "$(bm uiIntegrationMode)" = native ] || fail "the native registration changed"

  operator=$(cat "$GRAFANA_TOKENS/operator.jwt")
  owner=$(cat "$GRAFANA_TOKENS/owner.jwt")
  editor=$(cat "$GRAFANA_TOKENS/editor.jwt")
  g="http://$release-grafana.$ns:3000"
  # The Grafana NetworkPolicy admits only booth-core's pods; these probes wear its label.
  as_core() { probe "$1" app.kubernetes.io/name=booth-core "$2"; }
  # Connection-level failures are retried (an HTTP response of any status is not, so a 401/403
  # still counts). Added after an empty probe result on 2026-09-28 that was put down to
  # NetworkPolicy timing; the empty output was more likely the kubectl attach race that probe()
  # now avoids. Kept as cheap insurance, not as a known need.
  C="curl -sS -m 5 --retry 15 --retry-delay 2 --retry-all-errors"

  step "Grafana admits a platform operator (/platform/operator) as Editor"
  out=$(as_core g-operator "$C -H 'X-Booth-Identity: $operator' $g/api/user/orgs")
  echo "$out" | grep -q '"role":"Editor"' || fail "operator not admitted as Editor: $out"

  step "Grafana refuses an owner without /platform/operator outright (ADR 0077/0094)"
  out=$(as_core g-owner "$C -o /dev/null -w 'status=%{http_code}' -H 'X-Booth-Identity: $owner' $g/api/user/orgs")
  echo "$out" | grep -qE 'status=40[13]' || fail "owner without /platform/operator not refused: $out"

  step "Grafana refuses an editor outright"
  out=$(as_core g-editor "$C -o /dev/null -w 'status=%{http_code}' -H 'X-Booth-Identity: $editor' $g/api/user/orgs")
  echo "$out" | grep -qE 'status=40[13]' || fail "editor not refused: $out"

  step "an operator queries the real Loki through Grafana"
  body='{"from":"now-15m","to":"now","queries":[{"refId":"A","datasource":{"uid":"booth-loki"},"expr":"{module=\"itprobe\"} |= \"'"$marker"'\"","queryType":"range","maxLines":10}]}'
  out=$(as_core g-query "$C -H 'X-Booth-Identity: $operator' -H 'Content-Type: application/json' -H 'Origin: $g' -d '$body' $g/api/ds/query")
  echo "$out" | grep -q "$marker plain" || fail "Grafana query didn't return the probe's lines: $(echo "$out" | head -c 400)"

  step "Grafana is closed to pods other than booth-core's"
  out=$(probe g-stranger "" "curl -s -m 5 -o /dev/null -w 'status=%{http_code}' $g/api/health; echo")
  echo "$out" | grep -q "status=000" || fail "an unrelated pod reached Grafana: $out"
fi

echo "PASS"
