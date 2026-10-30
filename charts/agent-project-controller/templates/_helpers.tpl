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

{{/* Prefix for cluster-scoped objects of this release, such as default classes. */}}
{{- define "apc.clusterPrefix" -}}
{{- printf "%s-%s" .Release.Namespace .Release.Name | trunc 48 | trimSuffix "-" }}
{{- end -}}

{{/* CloudNativePG Cluster name for the controller database. */}}
{{- define "apc.databaseCluster" -}}
{{- default (printf "%s-db" .Release.Name) .Values.database.cloudnativepg.name | trunc 50 | trimSuffix "-" }}
{{- end -}}

{{/* Secret with the database URI, and the key. */}}
{{- define "apc.databaseSecret" -}}
{{- if eq .Values.database.mode "external" }}{{ required "database.external.existingSecret is required" .Values.database.external.existingSecret }}{{ else }}{{ include "apc.databaseCluster" . }}-app{{ end }}
{{- end -}}
