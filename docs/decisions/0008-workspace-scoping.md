# 0008: Per-workspace log scoping (ADR 0077) — how it's built

Status: **implemented.** `booth-architecture` ADR 0077 is the authoritative record of the
policy. This records how it was built and the judgment calls it left to this module.

## The rule

- **Operators** are anyone whose verified token's groups claim holds `/platform/operator`
  (ADR 0094; before that, owners acting in an `access.workspaces` workspace). They read every
  line, platform-wide, whatever their role in the active workspace.
- **Every other owner** reads only lines whose `workspace` label is their active workspace. The
  pin is added server-side from the verified identity, never from the request.
- **Editors and viewers** read nothing.
- **Grafana** admits operators only.

## Where the workspace comes from, and why it can't come from a log line

1. **Collector** (`collector-config.yaml`): one relabel rule copies the pod's
   `booth.projectbooth.io/workspace` label to `workspace`, validated as an ADR 0025 slug; any
   other value produces no label. This runs in pod discovery, per pod, before a line is read.
   The line-processing stage (`loki.process`) has only `stage.cri` (unwraps the runtime's
   framing) and `stage.label_drop`. There are no `json`/`logfmt`/`regex`/`labels` stages, so
   nothing a pod prints can become a label. `test/contract` pins both: exactly one rule sets
   `workspace`, and the processing stages are exactly those two.
2. **Query**: the pin is a **stream-selector** matcher (`{module=~"…", workspace="acme"}`),
   which matches only the labels a stream was ingested with. A pipeline filter
   (`| workspace="acme"`) would also match labels parsed from content or structured metadata.
3. **Response**: `internal/loki` now applies stream labels *after* any structured metadata
   Loki returns, so metadata derived from a line can add keys (`detected_level`) but never
   overwrite `module` or `workspace`. That was a latent issue (nothing produced a conflicting
   key); it's now tested.
4. **Fails closed**: the handlers read the scope from the request context, where
   `requireAccess` put it. A request without one is refused (403), never treated as
   platform-wide (`TestScope_MissingFailsClosed`).

**Verified against a real Loki** (`internal/api` `TestRealLoki_WorkspaceScoping`). A scoped
owner sees only their workspace's labelled line. They don't see another workspace's, and they
don't see unlabelled lines whose JSON or logfmt content claims their workspace, even when
searching for that text. The module list is scoped the same way. Removing the pin makes the
test fail, which it did when I checked. **Verified on a real cluster** (`verify.sh`): a pod
labelled `booth.projectbooth.io/workspace: acme` gets `workspace=acme`; an unlabelled pod
printing `{"workspace":"acme",…}` and `workspace=acme` gets no workspace label at all.

## How far the pod label itself can be trusted

The label is only as trustworthy as whoever can edit the pod. For booth-notebooks (the only
module setting it today) I checked:
- **User pods:** the spawner refuses to create a user pod that mounts a service-account token,
  so user code has no Kubernetes API access.
- **The hub:** its Role grants pods `get/list/watch/create/delete`, not `patch/update`, so it
  can't relabel a running pod.

Relabelling takes cluster-admin or equivalent rights in that namespace. Any future module
adopting the convention needs the same property: the process that sets the label must be the
only one able to change it.

## Judgment calls

1. ~~With no operators, Grafana isn't deployed at all.~~ Superseded by ADR 0094: the chart can
   no longer see who's an operator, so Grafana is deployed whenever it's enabled.
2. **Default install behaviour changed.** With no operators (now: nobody holding
   `/platform/operator`), no one sees shared-module logs; before ADR 0077, every owner saw
   everything. The startup log says how operators are identified.
3. **The UI says what a scoped owner is seeing.** `/api/config` now reports `scope`
   (`platform` or `workspace`) and the workspace. The viewer shows scoped owners a notice that
   they see only their workspace's own pods, and that shared platform services aren't included.

## Not covered

- **Shared-module logs about a tenant stay invisible to that tenant**, as ADR 0077 records.
- **booth-pipeline's job logs aren't per-workspace yet**; that waits on per-task pods (ADR 0057).
- **Nothing here has run behind a real booth-core, shell and booth-notebooks.** The scoping was
  checked against real Loki, a real cluster and real pod labels, but not with a real notebook
  pod spawned by the real hub.

## ADR 0094: operators from the `/platform/operator` claim (2026-09-30)

- **Identification.** `internal/auth` sets `Identity.PlatformOperator` when the verified token's
  groups claim contains exactly `/platform/operator`. It's read from the token only; no header
  can set it. An operator still needs a workspace context their token grants some role in (the
  gateway requires one); their role there doesn't matter. ADR 0067's old shape (an owner of a
  workspace named e.g. `platform`) grants nothing special any more. Everything else in this
  record is unchanged.
- **The chart value is gone.** `access.workspaces` and `BOOTH_LOGGING_ACCESS_WORKSPACES` are
  removed. A leftover `access.*` value **fails the render** with a migration message, rather
  than being silently ignored, so an upgrade surfaces it (checked on kind: a values-reusing
  upgrade of a release that had it set is refused). A leftover env var only logs a warning.
- **The default stays ADR 0077's**, not ADR 0067's "open with a warning" (which ADR 0094's text
  and this repo's brief still describe). A claim check has no configuration to detect "nobody
  qualifies" from, and ADR 0077 had already replaced that default.
- **Grafana** admits `contains(groups, '/platform/operator')`, strict, fail-closed. booth-core's
  `X-Booth-Identity` doesn't carry that entry yet (ADR 0094's amendment; booth-core's brief), so
  nobody is admitted until it does. `test/grafana` checks that today's core assertion shape (a
  workspace owner without the claim) is refused, and that only the exact string admits. The
  integration stub mints the future shape on purpose.
- **Grafana's plugin installer.** This work first ran into Grafana 13's background plugin
  installer breaking the Loki data source on the read-only root. The fix and its root cause (the
  installer updating Loki 13.2.0 to 13.2.1 from grafana.com) landed separately on `main`
  (`11d57f2`, see [0007](0007-grafana-view-implementation.md) section 5), and this migration builds
  on it.
