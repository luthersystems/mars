{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "fabric-peer.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "fabric-peer.fullname" -}}
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
{{- define "fabric-peer.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a qualified name for a PeristentVolumeClaim.
Pass the template a list of two elements as in the following example:
  {{ list "myvol" . | include "fabric-peer.pvc" }}
*/}}
{{- define "fabric-peer.pvc" -}}
{{- printf "%s-%s" (index . 0) (index . 1 | include "fabric-peer.fullname") | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a fully-qualified domain name for a peer in the current organization
(given by .Values.dlt.organization).
Pass the template a list of two elements, a peer index and the top-level
rendering context:
  {{ list 0 . | include "fabric-peer.fqdn" }}
  {{ list .Values.dlt.peerIndex . | include "fabric-peer.fqdn" }}
*/}}
{{- define "fabric-peer.fqdn" -}}
{{- $index := index . 0 -}}
{{- $ctx := index . 1 -}}
peer{{ $index }}.{{ $ctx.Values.dlt.organization}}.{{ $ctx.Values.dlt.domain }}
{{- end -}}

{{/*
Create a fully-qualified domain name for the peer being released (using
.Values.dlt.peerIndex).  This is a simplified shorthard for the common case of
the template "fabric-peer.fqdn"
*/}}
{{- define "fabric-peer.self-fqdn" -}}
{{- list .Values.dlt.peerIndex . | include "fabric-peer.fqdn" -}}
{{- end -}}


{{/*
Common labels
*/}}
{{- define "fabric-peer.labels" -}}
app.kubernetes.io/name: {{ include "fabric-peer.name" . }}
helm.sh/chart: {{ include "fabric-peer.chart" . }}
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
{{- define "fabric-peer.match-labels" -}}
app.kubernetes.io/name: {{ include "fabric-peer.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
fabric/organization: {{ .Values.dlt.organization }}
fabric/organization-index: {{ .Values.dlt.peerIndex | print | toJson }}
{{- end -}}

{{/*
Create the name of the fabric peer service account to use
*/}}
{{- define "fabric-peer.serviceAccountName" -}}
{{ default "default" .Values.serviceAccount.name }}
{{- end -}}
