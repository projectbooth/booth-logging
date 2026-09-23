# 0004: Loki runs as one small StatefulSet written in this chart, not Grafana's `loki` chart

Status: **ratified as built** — `booth-architecture` ADR 0068 (with 0003, 0005, 0006).

## Decision

`charts/booth-logging/templates/loki.yaml` runs Loki in single-binary mode (`-target=all`):
one replica, TSDB index and chunks on a `ReadWriteOnce` PersistentVolume, an in-memory ring,
and `auth_enabled: false` behind a NetworkPolicy. The config is about 50 lines, in
`loki-config.yaml`.

## Why not depend on Grafana's `loki` Helm chart

That chart targets scalable deployments on object storage. Even in its single-binary mode it
brings a gateway, memcached result/chunk caches, a canary and self-monitoring, plus hundreds
of values, most of which don't apply here. The same reasoning led booth-core to its own small
PostgreSQL StatefulSet over a subchart (ADR 0054). A config this short can also be tested
directly: `test/contract` asserts the chart's rendered config is identical to
`hack/loki-local.yaml`, which the Go tests run against.

## Limits this accepts for v0

- **One replica, local disk.** Loki is unavailable while its pod restarts. The collector
  buffers and retries meanwhile, so lines are delayed, not lost, unless the outage outlasts
  its retry window. There's no HA, which matches every other module running `replicaCount: 1`
  today (see `contracts/testing-strategy.md`'s multi-node deferral).
- **No object storage.** Capacity is the PVC. Moving to S3/GCS/Azure storage (and to
  Grafana's chart, if scale ever needs it) is a later, contained change. Note it would
  *not* go through booth-storage: that module's API is for workspace data, not a platform
  service's chunk store.
