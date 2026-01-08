{{/* vim: set filetype=mustache: */}}

{{- define "insideoutmcp.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "insideoutmcp.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := include "insideoutmcp.name" . -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "insideoutmcp.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "insideoutmcp.labels" -}}
app.kubernetes.io/name: {{ include "insideoutmcp.name" . }}
helm.sh/chart: {{ include "insideoutmcp.chart" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: mcp-server
{{- if .Values.global.partOf }}
app.kubernetes.io/part-of: {{ .Values.global.partOf }}
{{- end }}
{{- end -}}

{{- define "insideoutmcp.labels.match" -}}
app.kubernetes.io/name: {{ include "insideoutmcp.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "insideoutmcp.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "insideoutmcp.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end -}}

