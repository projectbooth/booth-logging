# 0007: How the Grafana view is built — judgment calls ADR 0076 left open

Status: **implemented, flagged to the coordinator.** ADR 0076 fixed the policy: Grafana's own
JWT auth over core's `X-Booth-Identity`, a binary ADR 0067 admission gate, and a second
`logging-grafana` registration. It left the mechanism to this module. The first item below is
a real mismatch between ADR 0076's wording and what Grafana accepts.

**Amended by ADR 0077 (2026-09-28):** Grafana now admits **operators only** (owners acting in an
`access.workspaces` workspace), and isn't deployed at all when there are none. Items 2 and 4
below are updated accordingly; see also [0008](0008-workspace-scoping.md).

## 1. Grafana reads core's keys from a file, not `jwk_set_url` (deviation from ADR 0076's wording)

ADR 0076 (and this task) said to point Grafana's `jwk_set_url` at core's iframe-identity JWKS.
**Grafana 13 refuses that URL:** it exits at startup with `jwt_set_url must have https scheme`
unless it runs in development mode. Core serves the issuer on its plain-http in-cluster
Service (`http://…svc.cluster.local:8080/iframe-identity`), the same URL booth-notebooks uses.
Found by running the real Grafana 13.2.2 against the rendered config (`test/grafana`).

What's built instead:

- The Grafana pod's **init container** (`booth-logging fetch-jwks`, this repo's own image) reads
  core's discovery document and checks that its `issuer` equals `grafana.identity.issuerUrl`
  **exactly**. If not, it fails with both spellings, catching ADR 0069's recorded
  `.svc` / `.svc.cluster.local` gotcha before Grafana starts. It then fetches `jwks_uri`, checks
  the set holds an RSA signing key, and writes it to a shared `emptyDir`. It retries while core
  is still starting (5 minutes), but not on an issuer mismatch.
- Grafana reads it with `[auth.jwt] jwk_set_file`. Signature, `iss` (via `expect_claims`),
  `aud` = `logging-grafana`, `exp`/`nbf` are all still verified by Grafana on every request.
  Only the delivery of the public keys changed.

**Cost:** the keys are fetched once per pod start. Core never rotates this key itself. It
creates it once and keeps it in the `booth-iframe-identity-keys` Secret; it changes only if
that Secret is deleted. If it does change, Grafana **fails closed** (refuses everyone) until its
pod restarts: `kubectl rollout restart deployment/<release>-grafana`. That's documented in
NOTES.txt and the README.

**Alternatives not taken:**
- `app_mode = development`: this lifts the https check but changes other Grafana behaviour, so
  it isn't acceptable in production.
- An https sidecar proxying core's JWKS: keys would stay live, but it adds a TLS certificate,
  `tls_client_ca` and a second long-running container for a key that doesn't rotate.
- Serving core's issuer over https: a booth-core change, which ADR 0076 said wasn't needed.

**For the coordinator:** worth a line in ADR 0076, or in ADR 0069's implementation notes, so
the next Grafana-based module (e.g. if Superset or Metabase take a similar route) doesn't
rediscover this.

## 2. The admission gate: `role_attribute_path` + `role_attribute_strict`, verified by breaking it

The gate is ADR 0076's first suggestion, not a pre-auth proxy. The chart renders a JMESPath
expression from the same `access.workspaces` and `oidc.groupsClaim` values the native viewer
uses. Since ADR 0077 it admits only a whole-string match of `/workspaces/<operator
workspace>/owner`; there's no "any owner" form any more. It evaluates to `grafana.admittedRole`
or `''`. With `role_attribute_strict = true`,
`''` refuses the login, and role sync runs on every request (`skip_org_role_sync = false`).
`access.workspaces` entries are now validated as workspace slugs, because they're spliced into
that expression.

`test/grafana` runs the real Grafana 13.2.2 with the chart's exact rendered `grafana.ini`, the
chart's read-only root filesystem, the real test Loki, and assertions signed like core's.
- **Refused:** an owner of a non-operator workspace (ADR 0077's case), editors, viewers, a
  missing or malformed groups claim, wrong `aud`, wrong `iss`, expired tokens, a foreign signing
  key, no header at all, and near-miss workspace slugs.
- **Admitted:** operators, who can query Loki.
- **Demotion:** a person who loses ownership is refused on their very next request.

The test was also run with the gate deliberately broken, and it failed both times:
- With `role_attribute_strict = false`, Grafana admitted editors, viewers and claimless tokens
  **as `Viewer`**. That's exactly the role downgrade ADR 0076 forbids, so the strict setting is
  load-bearing.
- With the expression widened to admit editors, the editor case failed.

## 3. Admitted people get `Editor`

ADR 0076 makes the gate binary but doesn't name the Grafana role. `Viewer` can't open Explore,
the ad-hoc LogQL surface this view exists for. `Editor` can, but can't manage data sources,
users or server settings. `test/grafana` checks that creating or deleting a data source and
reading admin settings are refused. The Loki data source is provisioned `editable: false`.
`grafana.admittedRole` accepts only `Viewer` or `Editor`; the chart refuses `Admin`. Editors
can save dashboards, but Grafana's state is an `emptyDir`, so they're lost when the pod
restarts.

## 4. Enabled by default, but deployed only with operators

`grafana.enabled: true`. Since ADR 0077, Grafana is actually deployed (workload, registration
and nav entry) only when `access.workspaces` names at least one operator workspace, and only
those operators are admitted. A default install, which has no operators, therefore has no
Grafana view. The earlier concern (any owner running arbitrary LogQL out of the box) no longer
applies.

## 5. Everything else Grafana could be entered by is off

There's no initial admin account (`disable_initial_admin_creation`), no login form, no basic
auth and no anonymous access. `allow_assign_grafana_admin = false`, sign-up is off, and
analytics, update checks and Grafana Live are off. `root_url` is under
`/iframe/logging-grafana/` with `serve_from_sub_path = false`, because core strips that prefix
before forwarding. That also keeps Grafana's own `/api/...` calls under the prefix and away
from booth-core's `/api/`.

## 6. NetworkPolicy: defense in depth, with an assumption about core's labels

As ADR 0076 suggests, Grafana admits ingress only from pods labelled
`app.kubernetes.io/name: booth-core`, in any namespace. That's booth-core's chart default, and
`grafana.allowedClients` overrides it. Loki's policy now also admits Grafana. If core's pods
are labelled differently, the view breaks visibly; access is never silently widened.
