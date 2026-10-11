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
Fails the render on an invalid shutdown setting. Both shutdown helpers call
it, so the rules hold whichever renders first.
*/}}
{{- define "connectorhub.validateShutdown" -}}
{{- $s := .Values.shutdown -}}
{{- $isInt := "^[0-9]+$" -}}
{{- if $s.preStop.enabled -}}
{{- if not (regexMatch $isInt (toString $s.preStop.sleepSeconds)) -}}
{{- fail (printf "shutdown.preStop.sleepSeconds must be a whole number of seconds, not %v" $s.preStop.sleepSeconds) -}}
{{- end -}}
{{- if lt (int $s.preStop.sleepSeconds) 1 -}}
{{- fail "shutdown.preStop.sleepSeconds must be at least 1 while shutdown.preStop.enabled" -}}
{{- end -}}
{{- $mode := toString ($s.preStop.mode | default "exec") -}}
{{- if not (has $mode (list "auto" "native" "exec")) -}}
{{- fail (printf "shutdown.preStop.mode must be auto, native or exec, not %q" $mode) -}}
{{- end -}}
{{- if not (regexMatch $isInt (toString $s.drainSeconds)) -}}
{{- fail (printf "shutdown.drainSeconds must be a whole number of seconds, not %v" $s.drainSeconds) -}}
{{- end -}}
{{- if lt (int $s.drainSeconds) 1 -}}
{{- fail "shutdown.drainSeconds must be at least 1 while shutdown.preStop.enabled" -}}
{{- end -}}
{{- end -}}
{{- $raw := toString $s.terminationGracePeriodSeconds -}}
{{- if and (not (kindIs "invalid" $s.terminationGracePeriodSeconds)) (ne $raw "") -}}
{{- if not (regexMatch $isInt $raw) -}}
{{- fail (printf "shutdown.terminationGracePeriodSeconds must be a whole number of seconds, not %s" $raw) -}}
{{- end -}}
{{- if and $s.preStop.enabled (le (int $raw) (int $s.preStop.sleepSeconds)) -}}
{{- fail (printf "shutdown.terminationGracePeriodSeconds (%s) must exceed shutdown.preStop.sleepSeconds (%d)" $raw (int $s.preStop.sleepSeconds)) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
The preStop hook's form: "native" (lifecycle.preStop.sleep) or "exec".
*/}}
{{- define "connectorhub.preStopMode" -}}
{{- include "connectorhub.validateShutdown" . -}}
{{- $mode := .Values.shutdown.preStop.mode | default "exec" -}}
{{- if eq $mode "auto" -}}
{{- if semverCompare ">=1.30-0" .Capabilities.KubeVersion.Version -}}native{{- else -}}exec{{- end -}}
{{- else -}}
{{- $mode -}}
{{- end -}}
{{- end -}}

{{/*
The pod's terminationGracePeriodSeconds, or empty for the Kubernetes default.
It covers the preStop sleep and the hub's drain after SIGTERM. An explicit
value, 0 included, is rendered as set.
*/}}
{{- define "connectorhub.terminationGracePeriodSeconds" -}}
{{- include "connectorhub.validateShutdown" . -}}
{{- $s := .Values.shutdown -}}
{{- $raw := toString $s.terminationGracePeriodSeconds -}}
{{- if and (not (kindIs "invalid" $s.terminationGracePeriodSeconds)) (ne $raw "") -}}
{{- int $raw -}}
{{- else if $s.preStop.enabled -}}
{{- add (int $s.preStop.sleepSeconds) (int $s.drainSeconds) -}}
{{- end -}}
{{- end -}}
