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

{{/* Validates telemetry.* and refuses the legacy config keys alongside it. */}}
{{- define "apc.telemetryValidate" -}}
{{- $t := .Values.telemetry }}
{{- if $t.enabled }}
{{- range $key := list "OTEL_EXPORTER_OTLP_ENDPOINT" "CELL_TELEMETRY_SCRAPER_JSON" "OTEL_EXPORTER_OTLP_HEADERS" }}
{{- if index $.Values.config $key }}
{{- fail (printf "config.%s is set by the chart when telemetry.enabled; use telemetry.*" $key) }}
{{- end }}
{{- end }}
{{- if eq (toString (index .Values.config "OTEL_ENABLED")) "true" }}
{{- fail "config.OTEL_ENABLED is set by the chart when telemetry.enabled; use telemetry.otlp.endpoint" }}
{{- end }}
{{- if and $t.otlp.endpoint (not (regexMatch "^https?://[^/?#]+(/[^?#]*)?$" ($t.otlp.endpoint | toString))) }}
{{- fail "telemetry.otlp.endpoint must be an http:// or https:// URL" }}
{{- end }}
{{- if and $t.otlp.headersSecret (not $t.otlp.endpoint) }}
{{- fail "telemetry.otlp.headersSecret needs telemetry.otlp.endpoint" }}
{{- end }}
{{- if and $t.collector.namespace (not $t.collector.podLabels) }}
{{- fail "telemetry.collector.podLabels is required with telemetry.collector.namespace" }}
{{- end }}
{{- if and $t.collector.podLabels (not $t.collector.namespace) }}
{{- fail "telemetry.collector.namespace is required with telemetry.collector.podLabels" }}
{{- end }}
{{- if $t.cells.enabled }}
{{- if not $t.collector.namespace }}
{{- fail "telemetry.cells.enabled needs telemetry.collector.namespace and podLabels (the pods that scrape each cell)" }}
{{- end }}
{{- if ne (default "cilium" (index .Values.config "NETWORK_PROVIDER" | toString)) "cilium" }}
{{- fail "telemetry.cells.enabled requires NETWORK_PROVIDER=cilium" }}
{{- end }}
{{- end }}
{{- if not (or $t.otlp.endpoint $t.cells.enabled $t.prometheus.podMonitors $t.dashboards.enabled) }}
{{- fail "telemetry.enabled needs at least one of otlp.endpoint, cells.enabled, prometheus.podMonitors or dashboards.enabled" }}
{{- end }}
{{- end }}
{{- end -}}

{{/* True when Glovecell processes push OTLP (telemetry.otlp.endpoint). */}}
{{- define "apc.otlpPush" -}}
{{- if and .Values.telemetry.enabled .Values.telemetry.otlp.endpoint }}true{{ end }}
{{- end -}}

{{/* OTLP env for a Glovecell process: (dict "root" $ "optionalHeaders" bool). */}}
{{- define "apc.otelEnv" -}}
{{- $root := .root }}
{{- if include "apc.otlpPush" $root }}
- name: OTEL_ENABLED
  value: "true"
- name: OTEL_EXPORTER_OTLP_ENDPOINT
  value: {{ $root.Values.telemetry.otlp.endpoint | quote }}
{{- with $root.Values.telemetry.otlp.headersSecret }}
- name: OTEL_EXPORTER_OTLP_HEADERS
  valueFrom:
    secretKeyRef:
      name: {{ . | quote }}
      key: {{ $root.Values.telemetry.otlp.headersKey | quote }}
{{- if $.optionalHeaders }}
      optional: true
{{- end }}
{{- end }}
{{- else if not $root.Values.telemetry.enabled }}
{{- range $key := list "OTEL_ENABLED" "OTEL_EXPORTER_OTLP_ENDPOINT" }}
{{- with index $root.Values.config $key }}
- name: {{ $key }}
  value: {{ . | quote }}
{{- end }}
{{- end }}
{{- end }}
{{- end -}}

{{/* TCP port of telemetry.otlp.endpoint (explicit, else 443 for https and 4318 for http). */}}
{{- define "apc.otlpPort" -}}
{{- $u := urlParse .Values.telemetry.otlp.endpoint }}
{{- $m := regexFind ":[0-9]+$" $u.host }}
{{- if $m }}{{ trimPrefix ":" $m }}{{ else if eq $u.scheme "https" }}443{{ else }}4318{{ end }}
{{- end -}}

{{/* Network peer selector for telemetry.collector (YAML list item body, no leading dash). */}}
{{- define "apc.collectorPeer" -}}
namespaceSelector:
  matchLabels:
    kubernetes.io/metadata.name: {{ .Values.telemetry.collector.namespace | quote }}
podSelector:
  matchLabels:
{{ toYaml .Values.telemetry.collector.podLabels | indent 4 }}
{{- end -}}

{{/* "true" when the CloudNativePG PodMonitor is on (its own switch or telemetry.prometheus.podMonitors). */}}
{{- define "apc.databasePodMonitor" -}}
{{- if or .Values.database.cloudnativepg.monitoring.enablePodMonitor (and .Values.telemetry.enabled .Values.telemetry.prometheus.podMonitors) }}true{{ else }}false{{ end }}
{{- end -}}
