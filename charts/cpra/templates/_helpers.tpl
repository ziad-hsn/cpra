{{- define "cpra.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "cpra.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "cpra.name" .) | trunc 48 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- define "cpra.selectorLabels" -}}
app.kubernetes.io/name: {{ include "cpra.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
{{- define "cpra.labels" -}}
{{ include "cpra.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
{{- end -}}
{{- define "cpra.image" -}}
{{- if .Values.image.digest -}}
{{ printf "%s@%s" .Values.image.repository .Values.image.digest }}
{{- else -}}
{{ printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) }}
{{- end -}}
{{- end -}}
{{- define "cpra.serviceAccount" -}}
{{- if .Values.serviceAccount.create -}}
{{ default (include "cpra.fullname" .) .Values.serviceAccount.name }}
{{- else -}}
{{ required "serviceAccount.name is required when serviceAccount.create=false" .Values.serviceAccount.name }}
{{- end -}}
{{- end -}}
{{- define "cpra.validate" -}}
{{- if .Values.api.enabled -}}
{{- $auth := required "auth.existingSecret must reference an immutable, versioned API-token Secret" .Values.auth.existingSecret -}}
{{- $secret := lookup "v1" "Secret" .Release.Namespace $auth -}}
{{- if and $secret (not $secret.immutable) -}}{{ fail "API-token Secret must be immutable; rotate through a new Secret name" }}{{- end -}}
{{- else if .Values.ingress.enabled -}}{{ fail "ingress requires api.enabled=true" }}{{- end -}}
{{- $sources := 0 -}}
{{- $envNames := dict -}}
{{- range .Values.providerEnv -}}
{{- if hasKey $envNames .name -}}{{ fail "providerEnv names must be unique" }}{{- end -}}
{{- $_ := set $envNames .name true -}}
{{- end -}}
{{- range (list .Values.manifest.existingSecret .Values.manifest.existingConfigMap .Values.manifest.existingClaim) -}}
{{- if . -}}{{- $sources = add1 $sources -}}{{- end -}}
{{- end -}}
{{- if ne (int $sources) 1 -}}{{ fail "manifest requires exactly one existingSecret, existingConfigMap, or existingClaim" }}{{- end -}}
{{- if ne .Values.persistence.enabled (eq .Values.storage.mode "raft") -}}{{ fail "raft requires persistence.enabled=true; disposable memory mode requires persistence.enabled=false" }}{{- end -}}
{{- if and (eq .Values.persistence.accessMode "ReadWriteOnce") (not .Values.persistence.acknowledgeReadWriteOnce) -}}{{ fail "ReadWriteOnce permits multiple pods on one node; explicitly acknowledge this weaker fence" }}{{- end -}}
{{- if and .Values.manifest.existingClaim (eq .Values.manifest.existingClaim .Values.persistence.existingClaim) -}}{{ fail "manifest and state must use separate claims" }}{{- end -}}
{{- if and .Values.ingress.enabled (or (not .Values.ingress.host) (not .Values.ingress.tlsSecret)) -}}{{ fail "ingress requires host and tlsSecret; plaintext ingress is unsupported" }}{{- end -}}
{{- if ne .Values.ingress.path "/" -}}{{ fail "the dashboard supports ingress path / only" }}{{- end -}}
{{- if and .Values.kubernetesRecovery.enabled (not (or .Values.kubernetesRecovery.restartDeployments .Values.kubernetesRecovery.scaleDeployments)) -}}{{ fail "kubernetesRecovery requires at least one designated Deployment" }}{{- end -}}
{{- end -}}
