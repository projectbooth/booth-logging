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
- **Workspace labels come from pod metadata only** (ADR 0077): a pod labelled
  `booth.projectbooth.io/workspace: acme` gets `workspace=acme` on its lines, and an unlabelled
  pod whose output claims `acme` (a JSON field and a logfmt pair) gets no workspace label.
- **The Grafana view** (ADR 0076), against a stub of booth-core's iframe-identity issuer
  ([`stubcore`](stubcore/main.go), served by [`deploy-stub-core.sh`](deploy-stub-core.sh)),
  because no real core runs here:
  - the `fetch-jwks` init container fetched the stub's keys and Grafana became ready;
  - the `logging-grafana` BoothModule registered as `iframe-proxy` while `logging` stayed
    `native`;
  - a platform operator's signed assertion (groups holding `/platform/operator`, ADR 0094; the
    stub mints the shape core will once ADR 0094's amendment ships) is admitted as `Editor` and
    queries the probe pod's lines from the real Loki (proving Loki's NetworkPolicy admits
    Grafana);
  - an owner's assertion without the claim (exactly what core mints today) and an editor's are
    refused outright;
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

The Grafana steps and the workspace-label steps above were also run on that local kind cluster
(2026-09-28) and passed.

## Against a real booth-core (`real-core` job, `REAL_CORE=1`)

[`realcore/deploy.sh`](realcore/deploy.sh) brings up the same stack booth-e2e's bring-up uses,
on kind:
- a real Keycloak in dev mode with [a test realm](realcore/realm-booth.json.tpl);
- a real booth-core, built from source at a pinned commit (`CORE_REF` in `integration.yml`;
  booth-core is a public repo, so no credential is needed) and installed from its own chart with
  booth-e2e's values;
- booth-logging trusting both. Grafana's issuer is left at the chart default, which must already
  match a `booth-core` release in `booth-system`.

`verify.sh`'s `REAL_CORE` section then runs, for each realm user, the real path: a password-grant
token, core's `/api/modules/logging-grafana/iframe-url`, the iframe entry (session cookie), and
Grafana's `/api/user/orgs` **through core's iframe proxy**. So core mints `X-Booth-Identity` from
the real token's groups, and Grafana verifies it against core's real keys.
- **Admitted as Editor:** an owner whose token carries `/platform/operator` (ADR 0094).
- **Refused outright (403):** an owner without the claim, and owners holding the near misses
  `/platform/operators` and `/platform/operator/readonly`.

On top of that, every other `verify.sh` step runs against the same release. That covers core's
controller reconciling both BoothModules to Healthy, and booth-logging's API verifying against a
real OIDC provider at startup.

**Run locally on 2026-10-07** against booth-core `264856f` (built from `git archive`), twice:
passed. **Negative control:** the same run against booth-core `8f0c6b4`, the commit before core
carried the claim, refuses the operator (403 `jwt.invalid_role`) and fails. So the check really
depends on core's fix.

## Not covered yet

- **The shell's nginx `/iframe/` routing and a real browser.** The real-core check talks to
  core's gateway directly, not through booth-design; booth-e2e covers the shell path.
- **booth-logging's own API with a real user token.** Its startup verifies against the real
  Keycloak, but no logs request is made with a Keycloak token. Owner/operator/scoping rules are
  covered in `internal/api`, including the full query path against a real Loki.

## The OIDC issuer used in CI

`https://accounts.google.com` is used only so the API's startup-time OIDC discovery
succeeds and the pod becomes ready. No login happens. It's a CI convenience, not a
recommendation (ADR 0004).
