{{/* Common labels and helpers */}}
{{- define "netmantle.name" -}}{{ default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}{{- end -}}

{{- define "netmantle.fullname" -}}
{{- printf "%s-%s" .Release.Name (include "netmantle.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "netmantle.labels" -}}
app.kubernetes.io/name: {{ include "netmantle.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "netmantle.selectorLabels" -}}
app.kubernetes.io/name: {{ include "netmantle.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
