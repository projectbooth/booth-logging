# Real-cluster integration tests (layer 3)

Per `contracts/testing-strategy.md` and ADR 0024. `.github/workflows/integration.yml` runs this
on merge to `main` and nightly. It deploys the chart into a kind cluster and runs
[`verify.sh`](verify.sh), which checks:

- The collector DaemonSet, Loki StatefulSet and API Deployment all become ready.
- The `BoothModule` reaches the API server with the declared id/navGroup/mode/health path,
  checked against a vendored copy of booth-core's CRD (`fixtures/`, copied from booth-core
  itself on 2026-09-22).
- **A pod with no logging integration at all has its output collected** (ADR 0022): both
  stdout and stderr; labelled `namespace`/`pod`/`container`/`stream`/`module` (with
  `booth-` stripped); Loki's `detected_level` correct for a JSON line and a logfmt line; CRI
  framing removed; and **exactly one** copy of each line even though the pod declares two
  ports (pod discovery yields one target per port).
- The chart's own pods are filed under `module=logging`.
- **Loki is unreachable from an unrelated pod** (NetworkPolicy), while the API reaches it.
- The API refuses unauthenticated requests (401).
- **The Grafana view** (ADR 0076), against a stub of booth-core's iframe-identity issuer
  ([`stubcore`](stubcore/main.go), served by [`deploy-stub-core.sh`](deploy-stub-core.sh)),
  because no real core runs here:
  - the `fetch-jwks` init container fetched the stub's keys and Grafana became ready;
  - the `logging-grafana` BoothModule registered as `iframe-proxy` while `logging` stayed
    `native`;
  - an owner's signed assertion is admitted as `Editor` and queries the probe pod's lines from
    the real Loki (proving Loki's NetworkPolicy admits Grafana);
  - an editor's assertion is refused outright;
  - a pod not labelled as booth-core can't reach Grafana at all.

## Where it has actually run

`verify.sh` was run against a local kind cluster (Kubernetes v1.37, kindnet) while this
module was built, and it passed. The same run found and fixed a real bug: Alloy running as
uid 0 with all capabilities dropped couldn't traverse the image's own `/var/lib/alloy`, which
the image owns under its alloy user. The GitHub workflow wrapping the script has since run
green on every push to `main`.

Retention was also checked by hand on that cluster: Loki's `/config` showed
`retention_period: 2w`, `max_query_lookback: 2w`, `retention_enabled: true`. Actual deletion
after 14 days isn't something a CI run can wait for.

The Grafana steps above were also run on that local kind cluster (2026-09-28), twice back to
back, and passed.

## Not covered yet

- **The Grafana view behind a real booth-core and shell**: loaded through core's iframe proxy,
  with core's real signing key and the shell's nginx `/iframe/` routing. The stub reproduces
  core's assertion shape (from `internal/iframeidentity`'s `Mint`), not core itself.
- **Deploying alongside a real, pinned booth-core**, going through its gateway, with its
  controller reconciling the `BoothModule` and polling `/healthz`. This is the same gap as
  booth-storage's (a cross-repo credential to pull core's pinned build). `booth-e2e` is the
  intended home for the cross-module path.
- **An authenticated request end-to-end** needs an OIDC provider in the cluster. Auth is
  covered at the unit layer (booth-storage's in-process IdP tests, copied with
  `internal/auth`). Owner/role/designated-workspace rules are covered in `internal/api`,
  including the full query path against a real Loki.

## The OIDC issuer used in CI

`https://accounts.google.com` is used only so the API's startup-time OIDC discovery
succeeds and the pod becomes ready. No login happens. It's a CI convenience, not a
recommendation (ADR 0004).
