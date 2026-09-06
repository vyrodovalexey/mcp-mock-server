{{/*
_helpers.tpl — names, labels, guards and resource-profile resolution.
All template-level guards (deployment.md §4.1) are evaluated from
_validate below, which is included once by the Deployment so a bad values
combination fails `helm template`/`helm install` with a named reason.
*/}}

{{/* Chart name, overridable. */}}
{{- define "mcpmock.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fully-qualified app name. */}}
{{- define "mcpmock.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Chart label value (name-version). */}}
{{- define "mcpmock.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Common labels applied to every object. */}}
{{- define "mcpmock.labels" -}}
helm.sh/chart: {{ include "mcpmock.chart" . }}
{{ include "mcpmock.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: mcpmock
{{- end -}}

{{/* Selector labels — the stable identity, never versioned. */}}
{{- define "mcpmock.selectorLabels" -}}
app.kubernetes.io/name: {{ include "mcpmock.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/* ServiceAccount name. */}}
{{- define "mcpmock.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "mcpmock.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* Name of the scenario ConfigMap actually mounted (rendered or referenced). */}}
{{- define "mcpmock.scenarioConfigMapName" -}}
{{- if .Values.scenario.configMapName -}}
{{- .Values.scenario.configMapName -}}
{{- else -}}
{{- printf "%s-scenario" (include "mcpmock.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/*
Effective resources: the `default` profile uses .Values.resources; any other
named profile pulls from .Values.profiles.<profile>.resources. An unknown
profile fails loudly.
*/}}
{{- define "mcpmock.resources" -}}
{{- if eq .Values.profile "default" -}}
{{- toYaml .Values.resources -}}
{{- else -}}
{{- $p := index .Values.profiles .Values.profile -}}
{{- if not $p -}}
{{- fail (printf "mcpmock: unknown profile %q (valid: default, large-fleet, many-streams, throughput)" .Values.profile) -}}
{{- end -}}
{{- toYaml $p.resources -}}
{{- end -}}
{{- end -}}

{{/* The memory limit string used to derive GOMEMLIMIT. */}}
{{- define "mcpmock.memoryLimit" -}}
{{- if eq .Values.profile "default" -}}
{{- .Values.resources.limits.memory -}}
{{- else -}}
{{- (index .Values.profiles .Values.profile).resources.limits.memory -}}
{{- end -}}
{{- end -}}

{{/*
_validate — all deployment.md §4.1 template guards. Called once from the
Deployment. Each `fail` names the reason so a bad install is self-explaining.
*/}}
{{- define "mcpmock.validate" -}}
{{/* Guard 1: hostile.enabled requires networkPolicy.enabled (ADR-018, MOCK-507.7). */}}
{{- if and .Values.hostile.enabled (not .Values.networkPolicy.enabled) -}}
{{- fail "mcpmock: hostile.enabled=true requires networkPolicy.enabled=true (ADR-018 containment / MOCK-507.7). Enable the NetworkPolicy or disable hostile mode." -}}
{{- end -}}
{{/* Guard 2: an off-loopback control service without a token Secret is refused. */}}
{{- if .Values.control.enabled -}}
{{- if or (eq (.Values.control.service.type | toString) "LoadBalancer") (eq (.Values.control.service.type | toString) "NodePort") -}}
{{- if not .Values.control.tokenSecretName -}}
{{- fail "mcpmock: control.service.type is LoadBalancer/NodePort but control.tokenSecretName is empty. The control API mutates the mock and reads its journal; an off-loopback bind requires a token (TASK-023). Set control.tokenSecretName to an existing Secret." -}}
{{- end -}}
{{- end -}}
{{/* A ClusterIP control service is still reachable in-cluster off-loopback; require a token there too. */}}
{{- if not .Values.control.tokenSecretName -}}
{{- fail "mcpmock: control.enabled=true but control.tokenSecretName is empty. The control API is reachable off-loopback in-cluster and the binary refuses a non-loopback bind without a token (TASK-023). Set control.tokenSecretName to an existing Secret, or leave control.enabled=false." -}}
{{- end -}}
{{- end -}}
{{/* Guard 3: mTLS client auth needs a CA secret. */}}
{{- if .Values.mcp.tls.enabled -}}
{{- if and (ne (.Values.mcp.tls.clientAuth | toString) "none") (not .Values.mcp.tls.clientCASecretName) -}}
{{- fail "mcpmock: mcp.tls.clientAuth requires a client CA but mcp.tls.clientCASecretName is empty." -}}
{{- end -}}
{{/* TLS termination in the chart is Phase 11; refuse rather than silently ignore. */}}
{{- fail "mcpmock: mcp.tls.enabled=true is not supported in Phase 1 (TLS is Phase 11). Terminate TLS at an ingress/gateway in front of the ClusterIP Service." -}}
{{- end -}}
{{/* Guard 5: exactly one of scenario.inline / scenario.configMapName. */}}
{{- $hasInline := gt (len (.Values.scenario.inline | default dict)) 0 -}}
{{- $hasRef := ne (.Values.scenario.configMapName | default "") "" -}}
{{- if and $hasInline $hasRef -}}
{{- fail "mcpmock: set exactly one of scenario.inline or scenario.configMapName, not both." -}}
{{- end -}}
{{- if and (not $hasInline) (not $hasRef) -}}
{{- fail "mcpmock: set exactly one of scenario.inline or scenario.configMapName; neither is set." -}}
{{- end -}}
{{- end -}}
