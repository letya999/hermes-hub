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
{{- define "hub.runtimeNamespace" -}}
{{- default (printf "%s-runtimes" .Release.Namespace) .Values.execution.kubernetes.namespace -}}
{{- end -}}
{{- define "hub.pullSecretNames" -}}
{{- range $i, $secret := .Values.imagePullSecrets -}}{{- if $i -}},{{- end -}}{{ $secret.name }}{{- end -}}
{{- end -}}
{{- define "hub.spaceRuntimeClass" -}}
{{- if .Values.execution.kubernetes.runtimeClassName -}}
{{- .Values.execution.kubernetes.runtimeClassName -}}
{{- else if ne .Values.execution.containerRuntime "runc" -}}
{{- .Values.execution.containerRuntime -}}
{{- else -}}
{{- .Values.runtimeClassName -}}
{{- end -}}
{{- end -}}
