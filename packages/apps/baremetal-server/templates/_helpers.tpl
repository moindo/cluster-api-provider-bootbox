{{- define "baremetal-server.name" -}}
{{- $prefix := printf "%s-" .Chart.Name -}}
{{- if not (hasPrefix $prefix .Release.Name) -}}
{{- fail (printf "baremetal-server: release name %q must start with %q" .Release.Name $prefix) -}}
{{- end -}}
{{- trimPrefix $prefix .Release.Name -}}
{{- end -}}

{{- define "baremetal-server.hostNamespace" -}}cozy-bootbox{{- end -}}

{{- /* Lets the dashboard list the objects under this application. */ -}}
{{- define "baremetal-server.appLabels" -}}
apps.cozystack.io/application.group: apps.cozystack.io
apps.cozystack.io/application.kind: BareMetalServer
apps.cozystack.io/application.name: {{ include "baremetal-server.name" . }}
internal.cozystack.io/tenantresource: "true"
{{- end -}}
