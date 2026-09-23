# 0006: The optional Grafana view is not in this first pass

Status: **deferral ratified** — `booth-architecture` ADR 0068. The brief marks the view optional
(ADR 0015's "Grafana as an optional iframe-proxied power-user view").

## Why not now

Adding Grafana is mostly chart work. The reason to hold off is access control. Grafana pointed
at Loki can run any LogQL over every tenant's logs, so it has to enforce at least the same rule
the native viewer does ([0002](0002-log-access-policy.md)). There are two plausible ways:

- Grafana's own OIDC login against the platform's identity provider, with a role mapping from
  the groups claim. That means a second login flow inside an iframe, and it's awkward with
  ADR 0005's token-in-query-parameter iframe mechanism.
- Grafana's `auth.proxy` mode, trusting identity headers from booth-core's gateway. By
  ADR 0041's reasoning that's only safe if Grafana can't be reached except through the
  gateway, and the gateway would have to forward a role Grafana can map.

Either choice depends on how 0002 is settled (who counts as a log reader), and the second
also depends on what core's iframe proxy forwards. Building one now would bake in an answer
to an open question. Leaving it out costs nothing structurally: the manifest's
`uiIntegrationMode` stays `native` for the basic viewer, and Grafana would be a separate
route or a second registration later.

Meanwhile the viewer shows the LogQL each search ran, so a power user with cluster access can
run the same query against Loki directly (e.g. `kubectl port-forward`).
