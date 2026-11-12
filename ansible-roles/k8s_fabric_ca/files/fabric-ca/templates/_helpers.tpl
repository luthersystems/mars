{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "fabric-ca.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "fabric-ca.fullname" -}}
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
Create a qualified name for a PeristentVolumeClaim.
Pass the template a list of two elements as in the following example:
  {{ list "myvol" . | include "fabric-ca.fullname" }}
*/}}
{{- define "fabric-ca.pvc" -}}
{{- printf "%s-%s" (index . 0) (index . 1 | include "fabric-ca.fullname") | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "fabric-ca.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "fabric-ca.labels" -}}
{{ include "fabric-ca.match-labels" . }}
helm.sh/chart: {{ include "fabric-ca.chart" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: {{ .Values.dlt.component }}
app.kubernetes.io/part-of: {{ .Values.global.partOf }}
{{- end -}}

{{/*
A subset of uniquely defining labels for selector.matchLabels
*/}}
{{- define "fabric-ca.match-labels" -}}
app.kubernetes.io/name: {{ include "fabric-ca.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
The internal domain name of the ca service.
*/}}
{{- define "fabric-ca.service-fqdn" -}}
ca.{{ .Values.dlt.domain }}
{{- end -}}

{{/*
Create the name of the fabric ca service account to use
*/}}
{{- define "fabric-ca.serviceAccountName" -}}
{{ default "default" .Values.serviceAccount.name }}
{{- end -}}

{{/*
Pod annotations checksum/<name>, one for each non-empty entry of
.Values.checksums. The k8s_fabric_ca role applies some objects the pod
reads at start, such as the crypto-config Secret, outside this chart. A pod
does not restart when such an object changes, and helm cannot see the
change. The role passes a hash of each object here, so a change to it changes
the pod template and the Deployment restarts the pod on the new data. An
unchanged object keeps the same hash and restarts nothing (#345, #346).
*/}}
{{- define "fabric-ca.checksum-annotations" -}}
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
