#!/usr/bin/env bash
# Deploys a stand-in for booth-core's iframe-identity issuer (ADR 0069) into the integration
# cluster, so the Grafana view's init container has real keys to fetch and verify.sh has signed
# assertions to present. The kind run has no real booth-core; see stubcore/main.go.
#
#   test/integration/deploy-stub-core.sh <namespace> <out-dir>
#
# Prints the issuer URL to pass as grafana.identity.issuerUrl. Assertions land in <out-dir>.
set -euo pipefail

ns=$1
out=$2
ctx=${KUBE_CONTEXT:+--context "$KUBE_CONTEXT"}
k() { kubectl $ctx "$@"; }

issuer="http://stub-core.$ns.svc.cluster.local:8080/iframe-identity"
mkdir -p "$out"
go run ./test/integration/stubcore -issuer "$issuer" -out "$out" >&2

k -n "$ns" create configmap stub-core --dry-run=client -o yaml \
  --from-file=openid-configuration="$out/openid-configuration" \
  --from-file=jwks.json="$out/jwks.json" | k apply -f - >&2

cat <<EOF | k apply -f - >&2
apiVersion: v1
kind: Pod
metadata:
  name: stub-core
  namespace: $ns
  labels:
    app: stub-core
spec:
  containers:
    - name: httpd
      image: busybox:1.36
      command: ["httpd", "-f", "-p", "8080", "-h", "/www"]
      ports: [{containerPort: 8080}]
      volumeMounts:
        - name: docs
          mountPath: /www/iframe-identity/.well-known
  volumes:
    - name: docs
      configMap:
        name: stub-core
---
apiVersion: v1
kind: Service
metadata:
  name: stub-core
  namespace: $ns
spec:
  selector:
    app: stub-core
  ports:
    - port: 8080
      targetPort: 8080
EOF
k -n "$ns" wait --for=condition=Ready pod/stub-core --timeout=120s >&2
echo "$issuer"
