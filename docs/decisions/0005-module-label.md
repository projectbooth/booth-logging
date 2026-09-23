# 0005: How a log line's "module" is derived — a convention, not a contract

Status: **ratified** — `booth-architecture` ADR 0068; the `booth.projectbooth.io/module` pod
label is now in `contracts/module-manifest.md`. The rest of this record is the original
reasoning, kept as written.

## The gap

The brief asks for logs "with pod/namespace/module metadata attached" and a viewer that
filters "by module". Kubernetes supplies pod and namespace metadata for free. Nothing supplies
the module: no contract says how a module's pods identify themselves, and ADR 0022 rules out
modules telling the logging system anything.

## Decision (v0)

The collector derives a `module` label from each pod. The first rule that matches wins:

1. pod label `booth.projectbooth.io/module` (explicit; this chart sets it on its own pods,
   so Loki's and Alloy's output files under `logging`);
2. pod label `app.kubernetes.io/name`, with a leading `booth-` removed. Every sibling chart
   already sets this to `booth-<id>`, so booth-storage's pods become `storage`, matching its
   manifest id, with no change to any other repo;
3. pod label `app`;
4. the container name.

Non-Booth pods (CoreDNS, kube-proxy, the CNI) get a sensible name from rules 2–4 and show up in
the viewer too. That's deliberate: ADR 0022 collects every pod, and an operator debugging the
platform needs those.

Verified on a real kind cluster (`test/integration/verify.sh`): a pod labelled
`app.kubernetes.io/name: booth-itprobe` is filed under `itprobe`.

## For the coordinator

Rule 2 works today only because every chart happens to follow the `helm create` labelling
convention. If modules' pod labels ever change, logs silently re-file under a different
name. A cheap hardening: add one line to `contracts/module-manifest.md` (or the chart
conventions) saying a module's pods SHOULD carry `booth.projectbooth.io/module: <id>`. It's
optional, has no enforcement, and changes nothing for modules until they adopt it. Not done
here, because it's a contract edit.
