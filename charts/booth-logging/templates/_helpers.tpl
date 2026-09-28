{{/*
Standard name/label helpers, the same shape as the sibling booth-* charts.
*/}}

{{- define "booth-logging.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "booth-logging.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "booth-logging.lokiName" -}}
{{- printf "%s-loki" (include "booth-logging.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "booth-logging.collectorName" -}}
{{- printf "%s-collector" (include "booth-logging.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "booth-logging.lokiURL" -}}
{{- printf "http://%s:3100" (include "booth-logging.lokiName" .) -}}
{{- end -}}

{{/*
Common labels. booth.projectbooth.io/module tells the collector which module a pod's logs
belong to (see collector-configmap.yaml), so Loki's and Alloy's own output is filed under
"logging" rather than "loki"/"alloy".
*/}}
{{- define "booth-logging.labels" -}}
app.kubernetes.io/name: {{ include "booth-logging.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
booth.projectbooth.io/module: logging
{{- end -}}

{{/*
Selector labels for one component: call with (dict "ctx" $ "component" "api").
*/}}
{{- define "booth-logging.selectorLabels" -}}
app.kubernetes.io/name: {{ include "booth-logging.name" .ctx }}
app.kubernetes.io/instance: {{ .ctx.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{/*
Retention in hours, validated. loki.retentionDays must be a whole number >= 1: Loki needs
retention to be a multiple of its 24h index period, and a fractional value would otherwise
be truncated without anyone noticing.
*/}}
{{- define "booth-logging.retentionHours" -}}
{{- $days := .Values.loki.retentionDays -}}
{{- $s := toString $days -}}
{{- if not (regexMatch "^[1-9][0-9]*$" $s) -}}
{{- fail (printf "loki.retentionDays must be a whole number of days, at least 1 (got %v)" $days) -}}
{{- end -}}
{{- mul (atoi $s) 24 -}}
{{- end -}}

{{/*
access.workspaces, validated: every entry must be an ADR 0025 workspace slug. The list feeds both
the API's allowlist and the Grafana admission expression below, where an unvalidated value would
be spliced into a JMESPath string literal.
*/}}
{{- define "booth-logging.accessWorkspaces" -}}
{{- range .Values.access.workspaces -}}
{{- if not (regexMatch "^[a-z0-9-]+$" (toString .)) -}}
{{- fail (printf "access.workspaces entries must be workspace slugs matching ^[a-z0-9-]+$ (got %q)" (toString .)) -}}
{{- end -}}
{{- end -}}
{{- join "," .Values.access.workspaces -}}
{{- end -}}

{{- define "booth-logging.grafanaName" -}}
{{- printf "%s-grafana" (include "booth-logging.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Whether the Grafana view is deployed at all: grafana.enabled AND at least one operator workspace
in access.workspaces. ADR 0077 admits operators only, so with no operators nobody could ever use
it — rather than register a nav entry that refuses everyone, nothing is rendered.
*/}}
{{- define "booth-logging.grafanaActive" -}}
{{- if and .Values.grafana.enabled .Values.access.workspaces -}}true{{- end -}}
{{- end -}}

{{/*
The Grafana view's admission gate (ADR 0076, as amended by ADR 0077: operators only), as a
JMESPath expression for [auth.jwt] role_attribute_path. It evaluates to the one role every
admitted person gets (grafana.admittedRole) or to '' — and with role_attribute_strict = true,
'' refuses the login outright. There is no third outcome: nobody is admitted at a lower role.

The claims are core's X-Booth-Identity assertion (ADR 0069), whose groups claim carries exactly
one entry, the ACTIVE workspace's "/workspaces/<ws>/<role>" (ADR 0025). Admitted iff that entry
is "/workspaces/<operator workspace>/owner" — an owner acting in one of access.workspaces. Any
other owner is refused: Grafana runs arbitrary LogQL, which can't be pinned to one workspace.
Whole-string matches, so a near-miss slug never matches. An absent or malformed groups claim is
treated as [] (refused), never an evaluation error.
*/}}
{{- define "booth-logging.grafanaRoleAttributePath" -}}
{{- if not .Values.access.workspaces -}}
{{- fail "internal: the Grafana admission expression needs at least one operator workspace" -}}
{{- end -}}
{{- $groups := printf "(\"%s\" || `[]`)" .Values.oidc.groupsClaim -}}
{{- $checks := list -}}
{{- range (splitList "," (include "booth-logging.accessWorkspaces" .)) -}}
{{- $checks = append $checks (printf "contains(%s, '/workspaces/%s/owner')" $groups .) -}}
{{- end -}}
{{- printf "(%s) && '%s' || ''" (join " || " $checks) .Values.grafana.admittedRole -}}
{{- end -}}
