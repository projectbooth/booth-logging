# booth-logging

Project Booth's logging module (nav group **Manage**). Every pod on the cluster writes to
stdout/stderr as usual. A node-level collector ships all of it to **Grafana Loki**, and a
native viewer lets workspace owners browse, filter and search it by module, time range and
severity (ADR 0015, ADR 0022). Platform operators (`/platform/operator`, ADR 0094) see
everything; every other owner sees only their own workspace's pods (ADR 0077). A full
**Grafana** view (ADR 0076), for operators only, is registered next to it as a second module,
`logging-grafana`. Modules integrate nothing: there is no SDK, no ingestion API and no
configuration. Brief: `../booth-architecture/agent-briefs/logging.md`.

## What v0 delivers

| Definition-of-done item | Where |
|---|---|
| Node-level collector DaemonSet tailing every pod's stdout/stderr, with pod/namespace/container/module/stream labels | `charts/booth-logging/templates/collector*.yaml` (Grafana Alloy — [0003](docs/decisions/0003-collector-grafana-alloy.md)) |
| A Loki backend that other modules' logs reach automatically | `charts/booth-logging/templates/loki*.yaml` ([0004](docs/decisions/0004-loki-single-binary.md)) |
| Retention: short (14 days), configurable, not hardcoded | `loki.retentionDays` ([0001](docs/decisions/0001-retention-default.md)) |
| Native viewer: browse/filter/search by module, time range, severity | `web/` (`@projectbooth/logging-ui`), backed by `internal/api` |
| Per-workspace scoping (ADR 0077) | collector's `workspace` label, `internal/api` scope pin, [0008](docs/decisions/0008-workspace-scoping.md) |
| Manifest + health check | `templates/boothmodule.yaml`, `/healthz` (reports Loki's readiness) |
| CI per `contracts/testing-strategy.md` | `.github/workflows/` |
| *Optional* Grafana view (ADR 0076) | `templates/grafana*.yaml`, second `BoothModule` `logging-grafana` (iframe-proxy), [0007](docs/decisions/0007-grafana-view-implementation.md) |

## How it fits together

```
 ingest:         each node's /var/log/pods ──► Alloy (DaemonSet) ──push──► Loki

 native viewer:  browser ─► core gateway    /modules/logging/api/*    ─► booth-logging API ─► Loki
 Grafana view:   browser ─► core iframe proxy /iframe/logging-grafana/* ─► Grafana           ─► Loki
                 (core signs X-Booth-Identity; Grafana verifies it and admits operators only)

 Loki's NetworkPolicy admits only the collector, the API and Grafana.
```

- **Collector** (`collector-config.yaml`): discovers pods on its own node, tails their log
  files, unwraps the container runtime's CRI line format, and pushes to Loki. Labels:
  `namespace`, `pod`, `container`, `stream` (stdout/stderr), `module` (see
  [0005](docs/decisions/0005-module-label.md)) and, for a pod labelled
  `booth.projectbooth.io/workspace`, `workspace`. Every label comes from Kubernetes metadata;
  nothing a pod prints becomes a label.
- **Severity** is Loki's own `detected_level`, derived at ingest from a JSON or logfmt
  `level` field or a keyword in plain text. The viewer groups those values into
  Error (error/critical/fatal), Warn, Info, Debug (debug/trace) and Unknown. Modules writing
  JSON lines with a `level` field (as ADR 0022 recommends) are classified reliably.
- **API** (`internal/api`): read-only. It turns structured filters into LogQL
  (`internal/logql`) and never accepts raw LogQL, so user input can't rewrite a query. It
  queries Loki and returns one newest-first timeline with a paging cursor.

## The Grafana view (ADR 0076)

> **Platform operators only (ADR 0077, ADR 0094).** Anyone admitted can run **arbitrary LogQL
> over every tenant's logs**, which can't be pinned to one workspace. So only assertions whose
> groups hold `/platform/operator` are admitted; everyone else is refused outright.
> **Until booth-core carries that entry into its `X-Booth-Identity` assertion (ADR 0094's
> amendment), nobody is admitted.** That's expected, not broken. Turn the view off with
> `grafana.enabled=false`.

- **Registration:** a second `BoothModule`: `id: logging-grafana`,
  `uiIntegrationMode: iframe-proxy`, `navPath: /logging-grafana`, `navGroup: manage`, with its
  `serviceRef` pointing at the Grafana Service. The native viewer's `logging` registration is
  unchanged (module-manifest.md's multi-surface pattern).
- **Authentication:** Grafana's own JWT auth over the `X-Booth-Identity` assertion booth-core
  signs on every iframe-proxied request (ADR 0069). Grafana verifies the signature, `iss` and
  `aud = logging-grafana` on every request. There's no Grafana login, no admin account, no
  basic auth and no anonymous access.
- **Keys:** Grafana only fetches a JWKS over https, and core's issuer is plain in-cluster http.
  So the pod's `fetch-jwks` init container fetches core's keys at start and Grafana reads them
  from a file ([0007](docs/decisions/0007-grafana-view-implementation.md)).
  `grafana.identity.issuerUrl` must equal core's iframe-identity issuer **exactly**; the init
  container refuses to start Grafana otherwise, naming both spellings. If core's key is ever
  regenerated, Grafana refuses everyone until `kubectl rollout restart deploy/<release>-grafana`.
- **Admission** is binary (ADR 0077, ADR 0094). A holder of `/platform/operator` is admitted as
  `Editor`, which is what Explore needs. Everyone else, including workspace owners, is refused
  outright, never admitted at a lower role. Role sync runs on every request, so losing the claim
  takes effect immediately. Editors can't change
  data sources, users or settings.
- **Loki data source** is provisioned read-only at this release's Loki. Loki's NetworkPolicy
  admits Grafana. Grafana's own NetworkPolicy admits only booth-core's pods, as defense in
  depth; the signature is the real boundary.

## API

Reached through core's gateway at `/modules/logging/api/...`. Needs `Authorization: Bearer …`
and `X-Workspace`. A **platform operator** (`/platform/operator` in the verified token, ADR 0094,
whatever their role in the active workspace) queries platform-wide. Otherwise **owner role
only**. Every other owner's queries are pinned server-side to
`workspace="<active workspace>"`, from the verified identity, whatever the request says
([0008](docs/decisions/0008-workspace-scoping.md)). Errors are `{"error", "field"?}`.

| Route | |
|---|---|
| `GET /api/logs?module=&module=&level=&q=&start=&end=&limit=` | Newest first. `level` ∈ error, warn, info, debug, unknown (repeatable). `q` is a case-insensitive literal substring (≤500 chars). `start`/`end` are RFC 3339 or Unix ns; default is the last hour; `end` is exclusive. `limit` is 1–1000, default 200. Response: `{entries, nextCursor?, query}`. Pass `nextCursor` as `end` for the next page. |
| `GET /api/modules?start=&end=` | Module names with logs in the range (default: the whole retention window), from the caller's scope only |
| `GET /api/config` | `{retentionSeconds, maxQueryRangeSeconds, maxLimit, levels, scope, workspace?}`; `scope` is `platform` (operator) or `workspace` |
| `GET /healthz` · `GET /livez` | Readiness, which is Loki's readiness (what core polls) · liveness |

Queries longer than `queryMaxRange` are refused with 400. `queryMaxRange` defaults to the
retention window. When Loki itself fails, the API answers 502 (Loki rejected the query, with
its message), 504 (timeout) or 503 (unreachable).

## Stack

The backend uses **Go** with `chi` and `go-oidc`. `internal/auth` is booth-storage's copy,
including ADR 0041's token-derived role and ADR 0056's optional second issuer. The UI is
**React + TypeScript + Vite + Tailwind**, published as `@projectbooth/logging-ui` (ADR 0030).
It exports `LoggingApp`, which takes `{workspace, role, theme, getAccessToken}` (ADR 0031/0033).
Images are pinned to `grafana/loki:3.7.8`, `grafana/alloy:v1.19.2` and `grafana/grafana:13.2.2`.

```
cmd/logging/          entrypoint (logs JSON lines to stdout, like every module should)
internal/api/         HTTP routes, access policy, validation
internal/logql/       structured filter → LogQL (the injection boundary)
internal/loki/        read-only Loki client; lokitest/ runs tests against a real Loki
internal/jwksfetch/   the Grafana pod's init container: fetch + check core's iframe-identity keys
internal/auth/        OIDC verification + token-derived role (from booth-storage)
web/                  the native viewer package
charts/booth-logging/ API Deployment, Loki StatefulSet, collector DaemonSet, Grafana, NetworkPolicies,
                      two BoothModules (logging, logging-grafana)
docs/decisions/       judgment calls the ADRs didn't settle — read these
test/contract/        manifest + chart checks (helm template)
test/grafana/         the Grafana admission gate against a real Grafana (docker)
test/integration/     verify.sh — real-cluster checks (kind), with a stub of core's issuer
hack/                 docker-compose Loki for the Go tests
```

## Running and testing

```sh
docker compose -f hack/docker-compose.loki.yml up -d      # a real Loki on :3100
eval "$(sh hack/test-env.sh)"                             # BOOTH_TEST_LOKI_URL
go test ./...                                             # unit + contract (+ real-Loki query path)
(cd web && npm ci && npm run typecheck && npm run lint && npm test -- --run && npm run build)
```

Without the Loki container the real-Loki tests **skip** locally, and so do `test/grafana`'s
real-Grafana tests, which also need docker. CI sets `BOOTH_TEST_STRICT=1`, which turns any
missing piece into a failure. `test/contract`
needs `helm`. Real-cluster checks: install the chart and run `test/integration/verify.sh`
(see `test/integration/README.md`).

CI: `ci.yml` (every push/PR: vet, `-race` tests against real Loki, `alloy fmt` on the
rendered collector config, web checks, helm lint, image build), `integration.yml` (kind,
merge-to-main and nightly), `publish.yml` (`logging-ui-v*` tags → npm), `release.yml`
(`v*.*.*` tags → image + chart). **Branch protection on `main` is a GitHub setting and has not
been configured from here.**

## Wiring into booth-design

Add `@projectbooth/logging-ui`, import `@projectbooth/logging-ui/dist/style.css` once, and
register it: `registerNativeModule("logging", LoggingApp)`.

## Read before deploying

- **Access** (ADR 0067 as amended by ADR 0077; operators per ADR 0094;
  [0008](docs/decisions/0008-workspace-scoping.md)): nothing to configure in this chart.
  **Platform operators** are whoever the identity provider grants the `/platform/operator`
  group; they read everything and get the Grafana view. Every other owner reads only their own
  workspace's labelled pods (notebook servers today). If nobody holds the group, nobody can read
  shared platform logs (core, storage, catalog, …). The old `access.workspaces` value was
  removed, and the chart refuses it if it's still set.
- **Workspace labels are an access boundary.** A pod labelled
  `booth.projectbooth.io/workspace: <ws>` is readable by that workspace's owners. Only the
  platform process that creates such pods should be able to set or change that label. Never put
  it on a shared module pod.
- **Grafana's issuer URL** (`grafana.identity.issuerUrl`) must match booth-core's iframe-identity
  issuer exactly. The default assumes core is release `booth-core` in namespace `booth-system`.
- **The collector runs as root** (uid 0, no capabilities, read-only root filesystem,
  `/var/log/pods` mounted read-only), because the kubelet's log files aren't world-readable. It
  holds a ClusterRole to get/list/watch **pods** (metadata only) and nothing else.
- **Loki has no authentication.** The NetworkPolicy is its access control. It needs a CNI that
  enforces NetworkPolicy (k3s and kind defaults do). With `networkPolicy.enabled=false` or a
  non-enforcing CNI, any pod can read or forge logs by talking to Loki directly.
- **Sizing:** `loki.persistence.size` (20Gi) is for a small cluster at 14 days. Everything every
  container prints is kept (ADR 0022), so a chatty workload fills it faster.
  `collector.excludeNamespaces` can drop whole namespaces.

## Not done / known limits

- **The Grafana view hasn't run behind a real booth-core and shell.** Its admission gate is
  tested against a real Grafana (docker and kind) with assertions signed like core's, but by a
  stand-in issuer. Nothing has yet loaded it through core's iframe proxy in a browser.
- **Grafana state is ephemeral** (`emptyDir`): dashboards someone saves are lost when the pod
  restarts.
- **Audit-style structured events:** nothing concrete has come up that needs a path beyond
  stdout, so none was built. ADR 0022 keeps this a separate concern. Nothing in this repo
  would block adding one later.
- **Not deployed alongside a real booth-core.** The integration run uses a vendored
  BoothModule CRD and a public OIDC discovery document. No authenticated request has gone
  through core's gateway to this API. The authenticated path is covered at the unit level
  (fake verifier) and through a real Loki.
- **Shared-module logs about a tenant stay invisible to that tenant** (ADR 0077): only
  per-workspace pods are scoped, and today that means booth-notebooks' user pods. booth-pipeline's
  job logs join once it runs per-task pods (ADR 0057).
- **The viewer UI hasn't been viewed in a browser.** It has component tests (jsdom) but no
  visual check against a live backend.
- **Search is a substring filter over the selected range.** Loki indexes labels, not content
  (ADR 0015's noted limit). Broad searches over many days are slow, which is why query range
  is capped.
- **Paging edge case:** lines sharing the exact nanosecond of a page boundary can be skipped
  (the cursor is an exclusive end timestamp). Negligible with runtime nanosecond timestamps.
- **Loki is single-replica on local disk** ([0004](docs/decisions/0004-loki-single-binary.md)).
- **No live tail.** The viewer shows a snapshot, and Refresh re-runs it.
