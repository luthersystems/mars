{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "fabric-cli.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "fabric-cli.fullname" -}}
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
{{- define "fabric-cli.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a qualified name for a PeristentVolumeClaim.
Pass the template a list of two elements as in the following example:
  {{ list "myvol" . | include "fabric-cli.pvc" }}
*/}}
{{- define "fabric-cli.pvc" -}}
{{- printf "%s-%s" (index . 0) (index . 1 | include "fabric-cli.fullname") | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a fully-qualified domain name for a peer in the current organization
(given by .Values.dlt.organization).
Pass the template a list of two elements, a peer index and the top-level
rendering context:
  {{ list 0 . | include "fabric-cli.peer-fqdn" }}
*/}}
{{- define "fabric-cli.peer-fqdn" -}}
{{- $index := index . 0 -}}
{{- $ctx := index . 1 -}}
peer{{ $index }}.{{ $ctx.Values.dlt.organization}}.{{ $ctx.Values.dlt.domain }}
{{- end -}}


{{/*
Common labels
*/}}
{{- define "fabric-cli.labels" -}}
app.kubernetes.io/name: {{ include "fabric-cli.name" . }}
helm.sh/chart: {{ include "fabric-cli.chart" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: {{ .Values.dlt.component }}
app.kubernetes.io/part-of: {{ .Values.global.partOf }}
fabric/organization: {{ .Values.dlt.organization }}
fabric/organization-index: {{ .Values.dlt.peerIndex | print | toJson }}
{{- end -}}

{{/*
A subset of uniquely defining labels for selector.matchLabels
*/}}
{{- define "fabric-cli.match-labels" -}}
app.kubernetes.io/name: {{ include "fabric-cli.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
fabric/organization: {{ .Values.dlt.organization }}
fabric/organization-index: {{ .Values.dlt.peerIndex | print | toJson }}
{{- end -}}

{{/*
Create the name of the fabric cli service account to use
*/}}
{{- define "fabric-cli.serviceAccountName" -}}
{{ default "default" .Values.serviceAccount.name }}
{{- end -}}
