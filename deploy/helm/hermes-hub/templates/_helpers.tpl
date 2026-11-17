{{- define "hub.name" -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 45 | trimSuffix "-" -}}
{{- end -}}
{{- define "hub.selector" -}}
app.kubernetes.io/instance: {{ .root.Release.Name | quote }}
app.kubernetes.io/name: hermes-hub
app.kubernetes.io/component: {{ .name | quote }}
{{- end -}}
{{- define "hub.image" -}}
{{- if .digest -}}
{{ .repository }}@{{ .digest }}
{{- else -}}
{{ .repository }}:{{ .tag }}
{{- end -}}
{{- end -}}
