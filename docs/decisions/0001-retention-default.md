# 0001: Default retention is 14 days, set in one chart value

Status: **implemented**. Applies the coordinator's ruling of 2026-09-22 ("short, 7–14 days,
configurable, not hardcoded"). Choosing the number within that range was left to this module.

## Decision

`loki.retentionDays: 14` in `charts/booth-logging/values.yaml`. That one value renders:

- Loki's `limits_config.retention_period`, which the compactor enforces
  (`compactor.retention_enabled: true`; without that flag Loki ignores the retention period
  and keeps logs forever);
- `limits_config.max_query_lookback`, so queries never reach past what's kept;
- `limits_config.reject_old_samples_max_age`, so Loki refuses lines already older than the
  retention window (e.g. a node's backlog after a long outage) instead of storing them for a
  moment and then deleting them;
- the API's `BOOTH_LOGGING_RETENTION`, which it reports to the UI and uses as the default cap
  on a single query's range.

It must be a whole number of days ≥ 1. The chart refuses to render anything else, because
Loki needs retention to be a multiple of its 24h index period and a fractional value would
otherwise be truncated without anyone noticing. `test/contract` pins the default, checks that
all four settings follow the value, and checks that retention is actually enabled.

## Why 14 and not 7

These logs are for operational debugging, not an audit trail. The debugging that needs them
often starts late: something that began before a long weekend or a week off, or a regression
someone wants to compare against the same time last week. Seven days loses exactly that
comparison. The cost of the second week is disk. ADR 0022 captures all container output by
default, but Loki stores it compressed (commonly about 10× smaller), so on a small cluster the
second week is a few GB. The default PVC (`loki.persistence.size: 20Gi`) is sized for 14 days
on a small cluster. The values file says to scale it as roughly daily compressed volume ×
retention.

## Changing it later

Changing retention later means setting `loki.retentionDays` and upgrading. The Loki pod rolls
because the config checksum changes. The compactor applies a shorter window on its next pass.
A longer window only helps data that hasn't been deleted yet. It can't bring back logs that
are already gone.
