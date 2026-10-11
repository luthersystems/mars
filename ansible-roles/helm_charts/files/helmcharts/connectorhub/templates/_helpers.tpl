{{/* vim: set filetype=mustache: */}}
{{/*
Expand the name of the chart.
*/}}
{{- define "connectorhub.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "connectorhub.fullname" -}}
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
{{- define "connectorhub.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "connectorhub.labels" -}}
app.kubernetes.io/name: {{ include "connectorhub.name" . }}
helm.sh/chart: {{ include "connectorhub.chart" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Label selector
*/}}
{{- define "connectorhub.labels.match" -}}
app.kubernetes.io/name: {{ include "connectorhub.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "connectorhub.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "connectorhub.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}


{{/*
Create a qualified name for a PeristentVolumeClaim.
Pass the template a list of two elements as in the following example:
  {{ list "myvol" . | include "connectorhub.fullname" }}
*/}}
{{- define "connectorhub.pvc" -}}
{{- printf "%s-%s" (index . 0) (index . 1 | include "connectorhub.fullname") | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
The preStop hook's form: "native" (lifecycle.preStop.sleep) or "exec".
*/}}
{{- define "connectorhub.preStopMode" -}}
{{- $p := .Values.shutdown.preStop -}}
{{- $mode := $p.mode | default "auto" -}}
{{- if lt (int $p.sleepSeconds) 1 -}}
{{- fail "shutdown.preStop.sleepSeconds must be at least 1 while shutdown.preStop.enabled" -}}
{{- end -}}
{{- if eq $mode "auto" -}}
{{- if semverCompare ">=1.30-0" .Capabilities.KubeVersion.Version -}}native{{- else -}}exec{{- end -}}
{{- else if or (eq $mode "native") (eq $mode "exec") -}}
{{- $mode -}}
{{- else -}}
{{- fail (printf "shutdown.preStop.mode must be auto, native or exec, not %q" $mode) -}}
{{- end -}}
{{- end -}}

{{/*
The pod's terminationGracePeriodSeconds, or empty for the Kubernetes default.
It covers the preStop sleep and the hub's drain after SIGTERM.
*/}}
{{- define "connectorhub.terminationGracePeriodSeconds" -}}
{{- $s := .Values.shutdown -}}
{{- $sleep := 0 -}}
{{- if $s.preStop.enabled -}}
{{- $sleep = int $s.preStop.sleepSeconds -}}
{{- end -}}
{{- $set := toString ($s.terminationGracePeriodSeconds | default "") -}}
{{- if ne $set "" -}}
{{- $grace := int $set -}}
{{- if le $grace $sleep -}}
{{- fail (printf "shutdown.terminationGracePeriodSeconds (%d) must exceed shutdown.preStop.sleepSeconds (%d)" $grace $sleep) -}}
{{- end -}}
{{- $grace -}}
{{- else if $s.preStop.enabled -}}
{{- add $sleep (int $s.drainSeconds) -}}
{{- end -}}
{{- end -}}
