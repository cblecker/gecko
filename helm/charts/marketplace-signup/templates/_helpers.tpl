{{/*
Fail at render time on settings that would otherwise crash-loop the pods or
send mail to the wrong place. The backend validates the same values at startup.
*/}}
{{- define "marketplace-signup.validate" -}}
{{- $_ := required "image.tag is required" .Values.image.tag -}}
{{- $_ := required "spa.image.tag is required" .Values.spa.image.tag -}}
{{- $_ := required "gateway.hostname is required" .Values.gateway.hostname -}}
{{- $_ := required "serviceAccount.gcpServiceAccount is required" .Values.serviceAccount.gcpServiceAccount -}}
{{- $_ := required "config.oauthClientId is required" .Values.config.oauthClientId -}}
{{- $_ := required "config.workflowProjectId is required" .Values.config.workflowProjectId -}}
{{- $_ := required "config.workflowRegion is required" .Values.config.workflowRegion -}}
{{- if .Values.certificate.enabled -}}
{{- $_ := required "gcp.projectId is required when certificate.enabled" .Values.gcp.projectId -}}
{{- end -}}
{{- if .Values.config.notifyIntegration -}}
{{- $_ := required "config.notifyProjectId is required when config.notifyIntegration is set" .Values.config.notifyProjectId -}}
{{- $_ := required "config.notifyRegion is required when config.notifyIntegration is set" .Values.config.notifyRegion -}}
{{- end -}}
{{- if .Values.config.notifyProjectId -}}
{{- if not (regexMatch "^[a-z][a-z0-9-]{4,28}[a-z0-9]$" .Values.config.notifyProjectId) -}}
{{- fail (printf "config.notifyProjectId is not a valid GCP project ID: %q" .Values.config.notifyProjectId) -}}
{{- end -}}
{{- end -}}
{{- if .Values.config.notifyRegion -}}
{{- if not (regexMatch "^[a-z]+-[a-z]+[0-9]+$" .Values.config.notifyRegion) -}}
{{- fail (printf "config.notifyRegion is not a valid GCP region: %q" .Values.config.notifyRegion) -}}
{{- end -}}
{{- end -}}
{{- end -}}
