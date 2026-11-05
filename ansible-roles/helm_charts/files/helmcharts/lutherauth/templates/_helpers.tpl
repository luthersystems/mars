{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "lutherauth.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "lutherauth.fullname" -}}
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

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "lutherauth.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "lutherauth.labels" -}}
app.kubernetes.io/name: {{ include "lutherauth.name" . }}
helm.sh/chart: {{ include "lutherauth.chart" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: lutherauth
app.kubernetes.io/part-of: {{ .Values.global.partOf }}
{{- end -}}

{{- define "lutherauth.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "lutherauth.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Pod annotations checksum/<name>, one for each non-empty entry of
.Values.checksums. The pod reads envFromSecret at start, and the umbrella
role applies that Secret outside this chart, so neither helm nor the pod
sees it change. The role passes a hash of it here: a new signing key or
OAuth client secret then restarts lutherauth at deploy time, not at the next
unrelated restart (a node drain), which would change the JWKS kid when no one
is watching (mars#346).
*/}}
{{- define "lutherauth.checksum-annotations" -}}
{{- $sums := dict -}}
{{- range $name, $sum := .Values.checksums -}}
{{- if $sum -}}
{{- $_ := set $sums $name $sum -}}
{{- end -}}
{{- end -}}
{{- with $sums -}}
annotations:
{{- range $name, $sum := . }}
  checksum/{{ $name }}: {{ $sum | quote }}
{{- end }}
{{- end -}}
{{- end -}}
