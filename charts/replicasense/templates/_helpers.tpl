{{- define "replicasense.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "replicasense.fullname" -}}
{{- if .Values.fullnameOverride }}{{ .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}{{ else }}{{ printf "%s-%s" .Release.Name (include "replicasense.name" .) | trunc 63 | trimSuffix "-" }}{{ end }}
{{- end }}

{{- define "replicasense.labels" -}}
app.kubernetes.io/name: {{ include "replicasense.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
{{- end }}

{{- define "replicasense.componentName" -}}
{{- printf "%s-%s" (include "replicasense.fullname" .root) .component | trunc 63 | trimSuffix "-" }}
{{- end }}
