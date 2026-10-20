{{/* Common labels. */}}
{{- define "gs.labels" -}}
app.kubernetes.io/part-of: gameserver
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}

{{- define "gs.operator" -}}{{ .Release.Name }}-operator{{- end -}}

{{/* Infrastructure addresses: in-chart services or external ones. */}}
{{- define "gs.natsURL" -}}
{{- if .Values.infra.enabled -}}nats://{{ .Release.Name }}-nats:4222{{- else -}}{{ .Values.gameCluster.externalInfra.natsURL }}{{- end -}}
{{- end -}}
{{- define "gs.redisAddr" -}}
{{- if .Values.infra.enabled -}}{{ .Release.Name }}-redis:6379{{- else -}}{{ .Values.gameCluster.externalInfra.redisAddr }}{{- end -}}
{{- end -}}
{{- define "gs.mongoURI" -}}
{{- if .Values.infra.enabled -}}mongodb://{{ .Release.Name }}-mongo:27017{{- else -}}{{ .Values.gameCluster.externalInfra.mongoURI }}{{- end -}}
{{- end -}}
