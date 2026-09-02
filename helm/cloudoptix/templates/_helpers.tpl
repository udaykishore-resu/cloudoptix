{{/*
Chart name and base labels — standard Helm chart conventions, kept in one
place so every template applies the exact same label set.
*/}}
{{- define "cloudoptix.name" -}}
{{- .Chart.Name -}}
{{- end -}}

{{- define "cloudoptix.fullname" -}}
{{- .Release.Name -}}
{{- end -}}

{{- define "cloudoptix.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" -}}
{{- end -}}

{{- define "cloudoptix.labels" -}}
app.kubernetes.io/name: {{ include "cloudoptix.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ include "cloudoptix.chart" . }}
{{- end -}}

{{/*
Per-component name and labels. $ is the top-level context, $name is the
components map key (e.g. "workerDiscovery"), $comp is that key's value.
Component names use kebab-case in Kubernetes object names regardless of the
values.yaml camelCase key, via the workerKind field for workers and a fixed
"api" for the API.
*/}}
{{- define "cloudoptix.componentName" -}}
{{- $name := index . 0 -}}
{{- $comp := index . 1 -}}
{{- if eq $comp.kind "worker" -}}
{{- printf "worker-%s" $comp.workerKind -}}
{{- else -}}
{{- $name -}}
{{- end -}}
{{- end -}}

{{- define "cloudoptix.componentFullname" -}}
{{- $root := index . 0 -}}
{{- $name := index . 1 -}}
{{- $comp := index . 2 -}}
{{- printf "%s-%s" (include "cloudoptix.fullname" $root) (include "cloudoptix.componentName" (list $name $comp)) -}}
{{- end -}}

{{- define "cloudoptix.componentLabels" -}}
{{- $root := index . 0 -}}
{{- $name := index . 1 -}}
{{- $comp := index . 2 -}}
{{ include "cloudoptix.labels" $root }}
app.kubernetes.io/component: {{ include "cloudoptix.componentName" (list $name $comp) }}
{{- end -}}

{{- define "cloudoptix.componentSelectorLabels" -}}
{{- $root := index . 0 -}}
{{- $name := index . 1 -}}
{{- $comp := index . 2 -}}
app.kubernetes.io/name: {{ include "cloudoptix.name" $root }}
app.kubernetes.io/instance: {{ $root.Release.Name }}
app.kubernetes.io/component: {{ include "cloudoptix.componentName" (list $name $comp) }}
{{- end -}}

{{/*
Service account name for one component — always a real k8s ServiceAccount
object (templates/serviceaccount.yaml), never "default", so IRSA's
eks.amazonaws.com/role-arn annotation and the sts:sub trust condition
terraform/modules/security renders always have a stable, per-component
subject to match against.
*/}}
{{- define "cloudoptix.componentServiceAccountName" -}}
{{- include "cloudoptix.componentFullname" . -}}
{{- end -}}

{{/*
Fully-qualified image reference for a component, honouring a per-component
override before falling back to global.imageRegistry/imageTag/imagePullPolicy.
*/}}
{{- define "cloudoptix.image" -}}
{{- $root := index . 0 -}}
{{- $comp := index . 1 -}}
{{- $registry := $root.Values.global.imageRegistry -}}
{{- $tag := $root.Values.global.imageTag | default $root.Chart.AppVersion -}}
{{- if and $comp.image $comp.image.repository -}}
{{- printf "%s:%s" $comp.image.repository ($comp.image.tag | default $tag) -}}
{{- else -}}
{{- printf "%s:%s" $registry $tag -}}
{{- end -}}
{{- end -}}

{{/*
DSN-shaped env vars every component (API and every worker alike) needs, as
a list consumed by `env:` in templates/deployment.yaml. Config values that
are NOT secret-shaped come from the ConfigMap via envFrom instead — this
list is only for values that need computation (Secret references) rather
than a flat copy from .Values.config.
*/}}
{{- define "cloudoptix.secretEnv" -}}
- name: CLOUDOPTIX_DATABASE_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "cloudoptix.fullname" . }}-secrets
      key: DATABASE_PASSWORD
- name: CLOUDOPTIX_REDIS_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "cloudoptix.fullname" . }}-secrets
      key: REDIS_PASSWORD
- name: CLOUDOPTIX_AWS_ASSUME_ROLE_EXTERNAL_ID
  valueFrom:
    secretKeyRef:
      name: {{ include "cloudoptix.fullname" . }}-secrets
      key: AWS_ASSUME_ROLE_EXTERNAL_ID
- name: CLOUDOPTIX_LLM_API_KEY
  valueFrom:
    secretKeyRef:
      name: {{ include "cloudoptix.fullname" . }}-secrets
      key: LLM_API_KEY
      optional: true
- name: CLOUDOPTIX_AUTH_SERVICE_TOKEN_SECRET
  valueFrom:
    secretKeyRef:
      name: {{ include "cloudoptix.fullname" . }}-secrets
      key: AUTH_SERVICE_TOKEN_SECRET
{{- end -}}
