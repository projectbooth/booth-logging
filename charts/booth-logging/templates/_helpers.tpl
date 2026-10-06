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
Refuses a leftover access.workspaces (or any access.*) value. ADR 0094 replaced that chart-value
operator list with the /platform/operator groups claim; silently ignoring an old value would
leave an operator wondering why their allowlist stopped working.
*/}}
{{- define "booth-logging.validate" -}}
{{- if .Values.access -}}
{{- fail "access.workspaces was removed (ADR 0094): platform operators are now identified by the /platform/operator entry in their token's groups claim, granted in the identity provider. Remove access.* from your values." -}}
{{- end -}}
{{- end -}}

{{- define "booth-logging.grafanaName" -}}
{{- printf "%s-grafana" (include "booth-logging.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Whether the Grafana view is deployed: grafana.enabled. (Before ADR 0094 it also needed a
configured operator list; operators are now a token claim, which the chart can't see, so
Grafana is deployed whenever it's enabled and admits whoever holds the claim.)
*/}}
{{- define "booth-logging.grafanaActive" -}}
{{- if .Values.grafana.enabled -}}true{{- end -}}
{{- end -}}

{{/*
The Grafana view's admission gate (ADR 0076; operators only per ADR 0077; operators identified
per ADR 0094), as a JMESPath expression for [auth.jwt] role_attribute_path. It evaluates to the
one role every admitted person gets (grafana.admittedRole) or to '' — and with
role_attribute_strict = true, '' refuses the login outright. There is no third outcome: nobody
is admitted at a lower role.

Admitted iff the assertion's groups claim contains exactly "/platform/operator". Grafana only
ever sees core's X-Booth-Identity assertion (ADR 0069), never the caller's own token, so this
works only once booth-core carries that entry into the assertion (ADR 0094's amendment; tracked
on booth-core's brief). Until then nobody is admitted — fail-closed, as the gate always was. An
absent or malformed groups claim is treated as [] (refused), never an evaluation error.
*/}}
{{- define "booth-logging.grafanaRoleAttributePath" -}}
{{- printf "contains((\"%s\" || `[]`), '/platform/operator') && '%s' || ''" .Values.oidc.groupsClaim .Values.grafana.admittedRole -}}
{{- end -}}
