# 0002: Who may read logs — owners only, optionally restricted to designated workspaces

Status: **ratified as built** — promoted to `booth-architecture` ADR 0067, which is the
authoritative record. The longer-term operator-role question stays open there.

## The gap

Every other module's data is scoped to a workspace (ADR 0008), and its access rules sit on
ADR 0025's `owner`/`editor`/`viewer` roles in that workspace. Logs don't fit that model.
ADR 0022 collects every container's output across the whole cluster, and one pod serves every
workspace: `booth-storage` is a single Deployment, and its log lines mention `acme` and
`globex` alike. So "your role in the active workspace" can't decide which lines you see.
Showing every line to anyone in some workspace would let a member of one tenant read about
another. The platform has no "platform admin" or "operator" role to fall back on. ADR 0025's
grammar knows only workspace roles.

## Decision (v0)

1. **Owner role required.** Owner is the strongest role that exists. The API checks it on
   every route, taking the role from the token's own groups claim, not the forwarded header
   (ADR 0041, using booth-storage's `internal/auth`). Editors and viewers get 403. The UI shows
   them an explanation and doesn't call the API.
2. **Optional restriction to designated workspaces.** `access.workspaces` (chart) /
   `BOOTH_LOGGING_ACCESS_WORKSPACES`. When set, only owners *acting in* one of those workspaces
   may read logs. That's typically a dedicated `platform`/`ops` workspace, which acts as a
   makeshift operator role.
3. **Default: open to the owner of any workspace**, with a `WARN` line at startup saying so.
   This follows ADR 0054's precedent for a protection that's opt-in: warn loudly and make it
   configurable. It makes a single-tenant install work out of the box. For a genuinely
   multi-tenant install it's the wrong default, which is why this is flagged.

Loki itself has no authentication. The chart's NetworkPolicy lets only the collector and this
API reach it, so this check can't be bypassed by querying Loki directly (provided the CNI
enforces NetworkPolicy; kind's and k3s's defaults do).

## For the coordinator

- Is "owner of any workspace" an acceptable default, or should it be fail-closed until an
  operator names designated workspaces?
- Longer term the platform probably wants a real operator/platform-admin role distinct from
  workspace roles. That's a cross-cutting change to ADR 0025's claim grammar, not something
  this module should invent. The designated-workspaces setting is a stopgap that needs no
  contract change.
- Per-workspace log filtering (showing a tenant only lines about their own workspace) isn't
  feasible from stdout capture. Lines carry no reliable workspace label, and ADR 0022
  deliberately gives modules no logging API to supply one.
