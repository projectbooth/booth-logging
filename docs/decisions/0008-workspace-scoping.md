# 0008: Per-workspace log scoping (ADR 0077) — how it's built

Status: **implemented.** `booth-architecture` ADR 0077 is the authoritative record of the
policy. This records how it was built and the judgment calls it left to this module.

## The rule

- **Operators** are owners acting in an `access.workspaces` workspace. They read every line,
  platform-wide.
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

1. **With no operators, Grafana isn't deployed at all.** ADR 0077 says nobody gets Grafana
   then. Rather than deploy one whose nav entry refuses everyone, the chart renders no Grafana
   workload, no `logging-grafana` registration and no Grafana NetworkPolicy unless
   `grafana.enabled` is true *and* `access.workspaces` is non-empty. NOTES.txt says so when
   Grafana is enabled but not deployed. Easy to reverse if you'd rather always register it.
2. **Default install behaviour changed.** With `access.workspaces: []` (still the default), no
   one sees shared-module logs any more; before ADR 0077, every owner saw everything. That
   follows directly from 0077, but operators upgrading will notice, and the startup log says so.
3. **The UI says what a scoped owner is seeing.** `/api/config` now reports `scope`
   (`platform` or `workspace`) and the workspace. The viewer shows scoped owners a notice that
   they see only their workspace's own pods, and that shared platform services aren't included.

## Not covered

- **Shared-module logs about a tenant stay invisible to that tenant**, as ADR 0077 records.
- **booth-pipeline's job logs aren't per-workspace yet**; that waits on per-task pods (ADR 0057).
- **Nothing here has run behind a real booth-core, shell and booth-notebooks.** The scoping was
  checked against real Loki, a real cluster and real pod labels, but not with a real notebook
  pod spawned by the real hub.
