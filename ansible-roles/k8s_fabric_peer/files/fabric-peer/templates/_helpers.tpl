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
Create the name of the operations Service: "<fullname>-ops", truncated to
63 chars. A 62 or 63 char fullname that ends in "-ops" truncates to the
fullname itself. Then the name is the fullname truncated to 54 chars, plus
"-" and the first 8 hex digits of the fullname's sha256. Other fullnames
keep "<fullname>-ops".
*/}}
{{- define "fabric-peer.ops-fullname" -}}
{{- $fullname := include "fabric-peer.fullname" . -}}
{{- $name := printf "%s-ops" ($fullname | trunc 59 | trimSuffix "-") -}}
{{- if eq $name $fullname -}}
{{- $name = printf "%s-%s" ($fullname | trunc 54 | trimSuffix "-") ($fullname | sha256sum | trunc 8) -}}
{{- end -}}
{{- $name -}}
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

{{- define "fabric-peer.chaincodeBuilderConfig" -}}
{{- printf "{\"index\": \"%d\"}" (int .Values.dlt.peerIndex) | quote }}
{{- end -}}

{{/*
The orderer org's domain: dlt.ordererDomain, or dlt.domain when unset.
*/}}
{{- define "fabric-peer.orderer-domain" -}}
{{- .Values.dlt.ordererDomain | default .Values.dlt.domain -}}
{{- end -}}

{{/*
The Fabric minor version ("2.5") of global.hyperledger.fabricVersion.
*/}}
{{- define "fabric-peer.fabric-minor" -}}
{{- regexFind "^[0-9]+\\.[0-9]+" (toString .Values.global.hyperledger.fabricVersion) -}}
{{- end -}}

{{/*
The image the peer builds Go chaincode in (CORE_CHAINCODE_BUILDER).

The upstream hyperledger/fabric-ccenv images trust the eMudhra (emSign)
certificates (CVE-2026-111943), and Fabric no longer releases 2.2 or 2.4,
so for the minors that luthersystems/buildenv wraps, the chart runs the
hardened luthersystems/fabric-ccenv:<minor>-<buildenv tag> by default
(luthersystems/mars#319). The tag is the minor, not the peer's patch
version, because buildenv wraps one patch per minor; Fabric's own default,
$(TWO_DIGIT_VERSION), also picks the image by minor. chaincodeImages.builder
names any image instead. An empty chaincodeImages.version, or a minor with
no wrapper, keeps the upstream buildImage.repository:<fabricVersion>.
*/}}
{{- define "fabric-peer.chaincode-builder" -}}
{{- $minor := include "fabric-peer.fabric-minor" . -}}
{{- $ci := .Values.chaincodeImages -}}
{{- if $ci.builder -}}
{{- $ci.builder -}}
{{- else if and $ci.version (has $minor (list "2.2" "2.4" "2.5")) -}}
{{- printf "luthersystems/fabric-ccenv:%s-%s" $minor $ci.version -}}
{{- else -}}
{{- printf "%s:%s" .Values.buildImage.repository (toString .Values.global.hyperledger.fabricVersion) -}}
{{- end -}}
{{- end -}}

{{/*
The image the peer runs Go chaincode in (CORE_CHAINCODE_GOLANG_RUNTIME), or
empty to keep the core.yaml default $(DOCKER_NS)/fabric-baseos:$(TWO_DIGIT_VERSION).

Only Fabric 2.2 and 2.4 get the hardened luthersystems/fabric-baseos: the
upstream 2.5 image has no CA bundle, so it has nothing to block and buildenv
does not wrap it. chaincodeImages.golangRuntime names any image instead.
*/}}
{{- define "fabric-peer.chaincode-golang-runtime" -}}
{{- $minor := include "fabric-peer.fabric-minor" . -}}
{{- $ci := .Values.chaincodeImages -}}
{{- if $ci.golangRuntime -}}
{{- $ci.golangRuntime -}}
{{- else if and $ci.version (has $minor (list "2.2" "2.4")) -}}
{{- printf "luthersystems/fabric-baseos:%s-%s" $minor $ci.version -}}
{{- end -}}
{{- end -}}
