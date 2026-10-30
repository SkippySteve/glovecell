{{/* Non-empty when the controller needs to exec into project workers. */}}
{{- define "apc.workerExec" -}}
{{- if or .Values.t3Connect.enabled .Values.controlClient.enabled }}true{{ end }}
{{- end -}}

{{/* ClusterRole the controller binds per project namespace for worker exec. */}}
{{- define "apc.workerExecRole" -}}
{{- printf "%s-%s" .Release.Namespace .Release.Name | trunc 51 | trimSuffix "-" }}-worker-exec
{{- end -}}

{{/* ClusterRole the controller binds per project namespace for that
project's Secrets and for deleting the project namespace itself. */}}
{{- define "apc.projectAccessRole" -}}
{{- printf "%s-%s" .Release.Namespace .Release.Name | trunc 48 | trimSuffix "-" }}-project-access
{{- end -}}

{{/* Prefix of the cluster-scoped admission policies and bindings. */}}
{{- define "apc.policyPrefix" -}}
{{- printf "%s-%s" .Release.Namespace .Release.Name | trunc 40 | trimSuffix "-" }}
{{- end -}}
