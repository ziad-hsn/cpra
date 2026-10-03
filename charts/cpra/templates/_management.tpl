{{- define "cpra.managementInit" -}}
- name: management-bootstrap
  image: {{ include "cpra.image" . | quote }}
  imagePullPolicy: {{ .Values.image.pullPolicy }}
  command: [/bin/sh, -ec]
  args:
    - |
      umask 077
      mkdir -p /work/protected
      chmod 0700 /work/protected
      cat /input/read-token > /work/protected/read-token
      cat /input/policy.yaml > /work/protected/policy.yaml
      cat /input/wrapping.key > /work/protected/wrapping.key
      cat /input/tls.crt > /work/protected/tls.crt
      cat /input/tls.key > /work/protected/tls.key
      cat /input/ca.crt > /work/protected/ca.crt
      chmod 0600 /work/protected/read-token /work/protected/policy.yaml /work/protected/wrapping.key /work/protected/tls.crt /work/protected/tls.key /work/protected/ca.crt
      {{- if .Values.management.encryption.previous }}
      mkdir -p /work/protected/previous
      chmod 0700 /work/protected/previous
      {{- range $index, $_ := .Values.management.encryption.previous }}
      cat /input/previous/{{ $index }}.key > /work/protected/previous/{{ $index }}.key
      chmod 0600 /work/protected/previous/{{ $index }}.key
      {{- end }}
      {{- end }}
  securityContext:
    allowPrivilegeEscalation: false
    readOnlyRootFilesystem: true
    capabilities:
      drop: [ALL]
  resources:
    requests: {cpu: 10m, memory: 32Mi}
    limits: {memory: 64Mi}
  volumeMounts:
    - name: management-inputs
      mountPath: /input
      readOnly: true
    - name: management-bootstrap
      mountPath: /work
{{- end -}}

{{- define "cpra.managementVolumes" -}}
- name: management-bootstrap
  emptyDir:
    medium: Memory
    sizeLimit: 16Mi
- name: management-inputs
  projected:
    defaultMode: 0440
    sources:
      - secret:
          name: {{ .Values.auth.existingSecret }}
          items:
            - key: {{ .Values.auth.key }}
              path: read-token
      - secret:
          name: {{ .Values.management.policy.existingSecret }}
          items:
            - key: {{ .Values.management.policy.key }}
              path: policy.yaml
      - secret:
          name: {{ .Values.management.encryption.existingSecret }}
          items:
            - key: {{ .Values.management.encryption.key }}
              path: wrapping.key
      - secret:
          name: {{ .Values.management.tls.existingSecret }}
          items:
            - key: {{ .Values.management.tls.keys.certificate }}
              path: tls.crt
            - key: {{ .Values.management.tls.keys.privateKey }}
              path: tls.key
            - key: {{ .Values.management.tls.keys.ca }}
              path: ca.crt
      {{- range $index, $source := .Values.management.encryption.previous }}
      - secret:
          name: {{ $source.existingSecret }}
          items:
            - key: {{ $source.key }}
              path: previous/{{ $index }}.key
      {{- end }}
{{- end -}}
