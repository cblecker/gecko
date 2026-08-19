{{/*
Expand the name of the chart.
*/}}
{{- define "platform-api-server.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "platform-api-server.fullname" -}}
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
{{- define "platform-api-server.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "platform-api-server.labels" -}}
helm.sh/chart: {{ include "platform-api-server.chart" . }}
{{ include "platform-api-server.selectorLabels" . }}
app.kubernetes.io/version: {{ .Values.image.tag | default .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "platform-api-server.selectorLabels" -}}
app.kubernetes.io/name: {{ include "platform-api-server.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "platform-api-server.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "platform-api-server.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Create the name of the migration service account to use
*/}}
{{- define "platform-api-server.migrationServiceAccountName" -}}
{{- if .Values.spanner.migration.serviceAccount.create }}
{{- default (printf "%s-migrate" (include "platform-api-server.fullname" .)) .Values.spanner.migration.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.spanner.migration.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Validate required values that must not remain as placeholders.
*/}}
{{- define "platform-api-server.validateValues" -}}
{{- $registry := trim (toString .Values.image.registry) -}}
{{- if or (not $registry) (eq $registry "CHANGE_ME") -}}
{{- fail "image.registry must be set (e.g. --set image.registry=quay.io)" -}}
{{- end -}}
{{- $repository := trim (toString .Values.image.repository) -}}
{{- if or (not $repository) (eq $repository "CHANGE_ME") -}}
{{- fail "image.repository must be set (e.g. --set image.repository=gecko/platform-api-server)" -}}
{{- end -}}
{{- if not (trim (toString .Values.image.tag)) -}}
{{- fail "image.tag must be set (e.g. --set image.tag=abc1234)" -}}
{{- end -}}
{{- if .Values.alloydb.enabled -}}
{{- if not (or .Values.alloydb.auth.password .Values.alloydb.auth.existingSecret) -}}
{{- fail "alloydb: either auth.password or auth.existingSecret must be set when alloydb is enabled" -}}
{{- end -}}
{{- end -}}
{{- if .Values.spanner.enabled -}}
{{- if not .Values.spanner.projectId -}}
{{- fail "spanner.projectId must be set when spanner is enabled" -}}
{{- end -}}
{{- if not .Values.spanner.instanceId -}}
{{- fail "spanner.instanceId must be set when spanner is enabled (the shared instance provisioned by the region Terraform module)" -}}
{{- end -}}
{{- if not .Values.spanner.databaseId -}}
{{- fail "spanner.databaseId must be set when spanner is enabled" -}}
{{- end -}}
{{- if .Values.alloydb.enabled -}}
{{- fail "spanner and alloydb cannot both be enabled; choose one storage backend" -}}
{{- end -}}
{{- end -}}
{{- end }}
