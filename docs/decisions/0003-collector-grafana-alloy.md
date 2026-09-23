# 0003: The collector is Grafana Alloy, not Promtail or Grafana Agent

Status: **ratified as built** — `booth-architecture` ADR 0068 (with 0004–0006). Departs from
the letter of ADR 0022 ("a Promtail or Grafana Agent DaemonSet") but keeps its intent.

## Why

Both tools ADR 0022 names are end-of-life as of this build (2026-09):

- **Grafana Agent**: long-term support ended; end-of-life **November 1, 2025**.
- **Promtail**: end-of-life **March 2, 2026**; no further updates or fixes.

Grafana names **Grafana Alloy** as the successor to both, and provides converters from both
config formats. Shipping a new platform component on an unmaintained collector, one that
reads every pod's log files as root on every node, isn't consistent with "production-ready
from the start".

## What stays the same

ADR 0022's substance is unchanged. The collector is a node-level DaemonSet that tails every
pod's container log files (`/var/log/pods`, read-only) and ships them to Loki, tagging each
line with pod/namespace/container metadata from the Kubernetes API. Modules integrate nothing.
Alloy runs the same pipeline Promtail would have (pod discovery → relabel → file tail → CRI
parse → push). Only the configuration language differs.

## Suggested follow-up

A one-line amendment to ADR 0022 (or a short superseding note) replacing "Promtail or Grafana
Agent" with "a node-level collector (Grafana Alloy)", so the next reader doesn't take the
named tools as current.
