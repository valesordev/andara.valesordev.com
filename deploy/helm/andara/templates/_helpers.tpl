{{- /*
Naming and label helpers. The StatefulSet is named `andara`, not `<release>-andara`,
because pod-0's DNS name (andara-0.andara.<ns>.svc) is written into AW-INF-006's server
certificate SANs and AW-INF-007's runbook; a release-prefixed name would make both depend
on how someone typed `helm install`.
*/ -}}
{{- define "andara.name" -}}andara{{- end -}}

{{- define "andara.labels" -}}
app: {{ include "andara.name" . }}
app.kubernetes.io/name: {{ include "andara.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Values.image.tag | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}

{{- define "andara.selectorLabels" -}}
app: {{ include "andara.name" . }}
app.kubernetes.io/name: {{ include "andara.name" . }}
{{- end -}}

{{- define "andara.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "andara.name" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- /*
The plain-text health and metrics port. `server.http.port` is the one server key the chart
must also know, because the probes point at it; everything else is opaque to the chart.
*/ -}}
{{- define "andara.httpPort" -}}
{{- dig "http" "port" 8080 (.Values.server | default dict) -}}
{{- end -}}

{{- /*
Resource requests: explicit values win; otherwise measurements.yaml × resourcesMultiplier,
rounded up. Limits are set only when given explicitly — a CPU limit on the tick loop is a
throttle nobody asked for, and a memory limit below the replica's footprint is an OOM
loop, so neither is derived.
*/ -}}
{{- define "andara.resources" -}}
{{- $m := .Files.Get "measurements.yaml" | fromYaml -}}
{{- $req := dig "requests" dict (.Values.resources | default dict) -}}
{{- $cpu := default (printf "%dm" (ceil (mulf $m.server.cpu_millicores_p99 .Values.resourcesMultiplier) | int)) $req.cpu -}}
{{- $mem := default (printf "%dMi" (ceil (mulf $m.server.memory_mib_p99 .Values.resourcesMultiplier) | int)) $req.memory -}}
requests:
  cpu: {{ $cpu | quote }}
  memory: {{ $mem | quote }}
{{- with dig "limits" nil (.Values.resources | default dict) }}
limits:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{- define "andara.projectorResources" -}}
{{- $m := (.root.Files.Get "measurements.yaml" | fromYaml) -}}
{{- $mp := index $m.projector .name -}}
{{- $req := dig "requests" dict (.spec.resources | default dict) -}}
{{- $cpu := default (printf "%dm" (ceil (mulf $mp.cpu_millicores_p99 .root.Values.resourcesMultiplier) | int)) $req.cpu -}}
{{- $mem := default (printf "%dMi" (ceil (mulf $mp.memory_mib_p99 .root.Values.resourcesMultiplier) | int)) $req.memory -}}
requests:
  cpu: {{ $cpu | quote }}
  memory: {{ $mem | quote }}
{{- with dig "limits" nil (.spec.resources | default dict) }}
limits:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}
