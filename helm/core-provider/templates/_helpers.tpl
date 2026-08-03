{{/*
Expand the name of the chart.
*/}}
{{- define "core-provider.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "core-provider.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "core-provider.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "core-provider.labels" -}}
helm.sh/chart: {{ include "core-provider.chart" . }}
{{ include "core-provider.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: krateo
{{- end }}

{{/*
Selector labels
*/}}
{{- define "core-provider.selectorLabels" -}}
app.kubernetes.io/name: {{ include "core-provider.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "core-provider.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "core-provider.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}


{{/*
Common labels
*/}}
{{- define "chart-inspector.labels" -}}
helm.sh/chart: {{ include "core-provider.chart" . }}
{{ include "chart-inspector.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: krateo
{{- end }}

{{/*
Selector labels
*/}}
{{- define "chart-inspector.selectorLabels" -}}
app.kubernetes.io/name: {{ include "core-provider.name" . }}-chart-inspector
app.kubernetes.io/instance: {{ .Release.Name }}-chart-inspector
{{- end }}

{{- define "chart-inspector.fullname" -}}
{{ include "core-provider.fullname" . }}-chart-inspector
{{- end }}

{{- define "chart-inspector.name" -}}
{{ include "core-provider.name" . }}-chart-inspector
{{- end }}

{{- define "chart-inspector.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "chart-inspector.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
