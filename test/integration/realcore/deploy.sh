#!/usr/bin/env bash
# Brings up a real booth-core and a real identity provider beside booth-logging, so verify.sh's
# REAL_CORE section can exercise the Grafana view's admission through booth-core's own iframe proxy
# and X-Booth-Identity minting (ADR 0069/0094), not the stub in ../stubcore.
#
#   test/integration/realcore/deploy.sh <booth-core checkout> <booth-core image> <booth-logging image>
#
# Same bring-up path as booth-e2e/bringup/bringup.sh: Keycloak in dev mode with a test realm, then
# booth-core from its own chart with booth-e2e's install values, then booth-logging. Both images must
# already be loaded into the cluster (pullPolicy Never). The test password is generated per run and
# kept in the Secret keycloak/realcore-test-password for verify.sh.
set -euo pipefail

core_dir=$1
core_image=$2
logging_image=$3
ctx=${KUBE_CONTEXT:+--context "$KUBE_CONTEXT"}
k() { kubectl $ctx "$@"; }
h() { helm ${KUBE_CONTEXT:+--kube-context "$KUBE_CONTEXT"} "$@"; }
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)

issuer="http://keycloak.keycloak.svc:8080/realms/booth"
core_url="http://booth-core.booth-system.svc:8080"

echo "--- Keycloak (realm with operator, plain owner and two near-miss users)"
k create namespace keycloak --dry-run=client -o yaml | k apply -f - >/dev/null
password=$(openssl rand -hex 12)
k -n keycloak create secret generic realcore-test-password --from-literal=password="$password" \
  --dry-run=client -o yaml | k apply -f - >/dev/null
k -n keycloak create secret generic keycloak-admin --from-literal=password="$(openssl rand -hex 12)" \
  --dry-run=client -o yaml | k apply -f - >/dev/null
sed "s/__TEST_PASSWORD__/$password/g" "$here/realm-booth.json.tpl" > "$here/.realm-booth.json"
k -n keycloak create configmap keycloak-realm --from-file=realm-booth.json="$here/.realm-booth.json" \
  --dry-run=client -o yaml | k apply -f - >/dev/null
rm -f "$here/.realm-booth.json"
k apply -f "$here/keycloak.yaml" >/dev/null
k -n keycloak rollout restart deploy/keycloak >/dev/null 2>&1 || true # re-import if it already existed
k -n keycloak rollout status deploy/keycloak --timeout=600s

echo "--- booth-core from $core_dir ($(git -C "$core_dir" rev-parse --short HEAD 2>/dev/null || echo '?'))"
k create namespace booth-system --dry-run=client -o yaml | k apply -f - >/dev/null
# Core's own CRD, applied explicitly: helm never updates a CRD that already exists, and a stale copy
# (e.g. this repo's vendored fixture) silently prunes fields it doesn't know.
k apply -f "$core_dir/charts/booth-core/crds/" >/dev/null
h upgrade --install booth-core "$core_dir/charts/booth-core" --namespace booth-system \
  --set oidc.issuerUrl="$issuer" --set oidc.clientId=booth-design \
  --set workloadIdentity.issuerUrl="$core_url" \
  --set-string iframeSigningKey="$(openssl rand -hex 32)" \
  --set image.repository="${core_image%:*}" --set image.tag="${core_image##*:}" --set image.pullPolicy=Never \
  --wait --timeout 10m >/dev/null
k -n booth-system rollout status deploy/booth-core --timeout=300s

echo "--- booth-logging, trusting that Keycloak and that booth-core"
# grafana.identity.issuerUrl is left at the chart default on purpose: it must already equal what a
# booth-core release named booth-core in booth-system publishes as its iframe-identity issuer.
k create namespace booth-logging --dry-run=client -o yaml | k apply -f - >/dev/null
h upgrade --install booth-logging "$repo/charts/booth-logging" --namespace booth-logging --reset-values \
  --set image.repository="${logging_image%:*}" --set image.tag="${logging_image##*:}" --set image.pullPolicy=Never \
  --set oidc.issuerUrl="$issuer" --set oidc.clientId=booth-design --set oidc.requireAudience=false \
  --wait --timeout 6m >/dev/null
# A restart picks up core's current keys in fetch-jwks even when the release was already installed.
k -n booth-logging rollout restart deploy/booth-logging-grafana >/dev/null
k -n booth-logging rollout status deploy/booth-logging-grafana --timeout=300s
echo "realcore deployed"
