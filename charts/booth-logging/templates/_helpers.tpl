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
