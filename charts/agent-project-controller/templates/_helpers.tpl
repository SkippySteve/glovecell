{{/* Non-empty when the controller needs to exec into project workers. */}}
{{- define "apc.workerExec" -}}
{{- if or .Values.t3Connect.enabled .Values.controlClient.enabled }}true{{ end }}
{{- end -}}

{{/* ClusterRole the controller binds per project namespace for worker exec. */}}
{{- define "apc.workerExecRole" -}}
{{- printf "%s-%s" .Release.Namespace .Release.Name | trunc 51 | trimSuffix "-" }}-worker-exec
{{- end -}}

{{/* ClusterRole the controller binds to the Orca hub per project namespace:
read worker pods and their logs (pairing offers) in cells only. */}}
{{- define "apc.hubLogsRole" -}}
{{- printf "%s-%s" .Release.Namespace .Release.Name | trunc 54 | trimSuffix "-" }}-hub-logs
{{- end -}}

{{/* True when the controller binds the hub's log read per project. */}}
{{- define "apc.hubLogs" -}}
{{- if and .Values.hubspoke.orca.enabled .Values.hubspoke.hub.logsPerProject }}true{{ end }}
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

{{/* Namespace of the forge agent (forgeAgent.namespace, default <release namespace>-forge). */}}
{{- define "apc.forgeAgentNamespace" -}}
{{- default (printf "%s-forge" .Release.Namespace | trunc 63 | trimSuffix "-") .Values.forgeAgent.namespace -}}
{{- end -}}

{{/* Namespace of the provider broker and credential owner (providerBroker.namespace, default <release namespace>-providers). */}}
{{- define "apc.providerBrokerNamespace" -}}
{{- default (printf "%s-providers" .Release.Namespace | trunc 63 | trimSuffix "-") .Values.providerBroker.namespace -}}
{{- end -}}

{{/* Placement of the provider broker, owner and login pods (called with .Values.providerBroker). */}}
{{- define "apc.providerBrokerScheduling" -}}
{{- with .scheduling.nodeSelector }}
nodeSelector:
{{ toYaml . | indent 2 }}
{{- end }}
{{- with .scheduling.tolerations }}
tolerations:
{{ toYaml . | indent 2 }}
{{- end }}
{{- with .scheduling.affinity }}
affinity:
{{ toYaml . | indent 2 }}
{{- end }}
{{- end -}}

{{/* The lease Secret the controller reads: the chart's broker when providerBroker.enabled, else config.PROVIDER_LEASES_SECRET. */}}
{{- define "apc.providerLeasesSecret" -}}
{{- if .Values.providerBroker.enabled }}{{ printf "%s/provider-leases" (include "apc.providerBrokerNamespace" .) }}{{ else }}{{ index .Values.config "PROVIDER_LEASES_SECRET" | default "" | toString }}{{ end }}
{{- end -}}
