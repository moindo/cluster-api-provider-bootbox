{{- define "baremetal.hostNamespace" -}}cozy-bootbox{{- end -}}
{{- define "baremetal.statusConfigMap" -}}baremetal-status{{- end -}}

{{- /* Lets the dashboard relate the objects to this module. */ -}}
{{- define "baremetal.appLabels" -}}
apps.cozystack.io/application.group: apps.cozystack.io
apps.cozystack.io/application.kind: BareMetal
apps.cozystack.io/application.name: {{ .Release.Name }}
internal.cozystack.io/tenantresource: "true"
{{- end -}}
