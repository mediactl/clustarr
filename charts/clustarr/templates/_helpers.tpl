{{/*
Name helpers -- standard Helm shapes.
*/}}
{{- define "clustarr.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "clustarr.fullname" -}}
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

{{- define "clustarr.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
The Secret holding the Torznab facade's API keys (indexarr
--facade-api-key-secret). indexarr creates it with one random key when it is
absent and never modifies it, so the chart only names it -- it does not
render it, and so `helm uninstall` leaves the key behind for a reinstall to
reuse. Set indexarr.facade.apiKeySecret to use a Secret you manage.
*/}}
{{- define "clustarr.facadeAPIKeySecret" -}}
{{- default (printf "%s-indexarr-facade" (include "clustarr.fullname" .)) .Values.indexarr.facade.apiKeySecret -}}
{{- end -}}

{{- define "clustarr.labels" -}}
helm.sh/chart: {{ include "clustarr.chart" . }}
app.kubernetes.io/name: {{ include "clustarr.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: clustarr
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Selector labels for one component. Call with (dict "root" $ "component" "catalogarr").
Immutable on a Deployment, so keep this to name/instance/component only --
adding the chart version here would break every upgrade.
*/}}
{{- define "clustarr.selectorLabels" -}}
app.kubernetes.io/name: {{ include "clustarr.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{/*
Image reference. Call with (dict "root" $ "which" "media"|"controller"|"mediaCuda").
*/}}
{{- define "clustarr.image" -}}
{{- $img := index .root.Values.image .which -}}
{{- $tag := default .root.Chart.AppVersion $img.tag -}}
{{- if .root.Values.image.registry -}}
{{- printf "%s/%s:%s" .root.Values.image.registry $img.repository $tag -}}
{{- else -}}
{{- printf "%s:%s" $img.repository $tag -}}
{{- end -}}
{{- end -}}

{{/*
NATS client URL: an explicit natsUrl wins, otherwise the bundled subchart's
service (the nats chart names its client service after the release).
*/}}
{{- define "clustarr.natsUrl" -}}
{{- if .Values.natsUrl -}}
{{- .Values.natsUrl -}}
{{- else -}}
{{- printf "nats://%s-nats.%s.svc:4222" .Release.Name .Release.Namespace -}}
{{- end -}}
{{- end -}}

{{/*
Whether the JetStream topology must be collapsed to one replica (R1 streams
and KV buckets). An explicit natsSingleNode wins; otherwise it is derived from
the bundled subchart, because R3 streams cannot be created on a NATS that is
not clustered three ways and every service would fail to start (§12).
*/}}
{{- define "clustarr.natsSingleNode" -}}
{{- if kindIs "bool" .Values.natsSingleNode -}}
{{- .Values.natsSingleNode -}}
{{- else if not .Values.nats.enabled -}}
false
{{- else if not (dig "config" "cluster" "enabled" false .Values.nats) -}}
true
{{- else if lt (int (dig "config" "cluster" "replicas" 3 .Values.nats)) 3 -}}
true
{{- else -}}
false
{{- end -}}
{{- end -}}

{{/*
GOMEMLIMIT: 80% of a Kubernetes memory quantity, in whole bytes (§12:
"Torrent engines: GOMEMLIMIT = 80% of limit"). The Downward API's
resourceFieldRef on a container's own limit only ever gives back 100% of it
-- there is no scaling operator -- so the 80% figure has to be computed once,
here, from the same quantity values.yaml already puts in
resources.limits.memory, and baked into the rendered env var as a literal.

This is applied to every Clustarr service the chart renders (clustarr.workload
below), which covers the grabarr *controller* pod but not the torrent/usenet
engine StatefulSets and Deployments it creates per DownloadClient at runtime
-- those get their resources from DownloadClient.spec.resources, a CRD field
this chart never sees, so the identical 80% calculation has to be made again
in grabarr/controller/downloadclient (Go, not a template); see the gap-fixes
X12a report.

Call with the raw resources.limits.memory string. Handles the binary
(Ki/Mi/Gi/Ti) and decimal (K/M/G/T) Kubernetes quantity suffixes and a bare
byte count -- every form this chart's own values.yaml uses and the ones
`kubectl` normally accepts for memory. Anything else (exponent form, a "m"
milli-suffix, which is meaningless for memory) fails the render rather than
silently emitting a wrong number.
*/}}
{{- define "clustarr.gomemlimit" -}}
{{- $q := . -}}
{{- $mult := 1.0 -}}
{{- $num := $q -}}
{{- if hasSuffix "Ki" $q -}}{{- $mult = 1024.0 -}}{{- $num = trimSuffix "Ki" $q -}}
{{- else if hasSuffix "Mi" $q -}}{{- $mult = 1048576.0 -}}{{- $num = trimSuffix "Mi" $q -}}
{{- else if hasSuffix "Gi" $q -}}{{- $mult = 1073741824.0 -}}{{- $num = trimSuffix "Gi" $q -}}
{{- else if hasSuffix "Ti" $q -}}{{- $mult = 1099511627776.0 -}}{{- $num = trimSuffix "Ti" $q -}}
{{- else if hasSuffix "K" $q -}}{{- $mult = 1000.0 -}}{{- $num = trimSuffix "K" $q -}}
{{- else if hasSuffix "M" $q -}}{{- $mult = 1000000.0 -}}{{- $num = trimSuffix "M" $q -}}
{{- else if hasSuffix "G" $q -}}{{- $mult = 1000000000.0 -}}{{- $num = trimSuffix "G" $q -}}
{{- else if hasSuffix "T" $q -}}{{- $mult = 1000000000000.0 -}}{{- $num = trimSuffix "T" $q -}}
{{- else if not (regexMatch "^[0-9]+$" $q) -}}
{{- fail (printf "clustarr.gomemlimit: %q is not a supported memory quantity (want a bare byte count, or a Ki/Mi/Gi/Ti/K/M/G/T suffix)" $q) -}}
{{- end -}}
{{- printf "%.0f" (mulf ($num | float64) $mult 0.8) -}}
{{- end -}}

{{/*
Guard rails. These are correctness invariants from the design spec, not
preferences, so they fail the render rather than warn.
*/}}
{{- define "clustarr.validate" -}}
{{- if ne (int .Values.catalogarrMetadata.replicas) 1 -}}
{{- fail "catalogarrMetadata.replicas must be exactly 1: the metadata gateway holds its provider rate-limiter windows in process memory, so a second replica doubles the outbound request rate against TMDB/TVDB/MusicBrainz and will get you banned, not throttled." -}}
{{- end -}}
{{- if ne (int .Values.indexarr.replicas) 1 -}}
{{- fail "indexarr.replicas must be exactly 1: the release index is a local SQLite database (WAL+FTS5) on a ReadWriteOnce PVC. A second replica cannot bind the volume and must not share the database." -}}
{{- end -}}
{{- if and (not .Values.storage.data.existingClaim) (ne .Values.storage.data.accessMode "ReadWriteMany") -}}
{{- fail "storage.data.accessMode must be ReadWriteMany: /data is one volume shared by catalogarr, grabarr, squasharr and captionarr, and import is a hardlink or rename inside that single filesystem. Use CephFS, or NFS/Longhorn RWX. Never exFAT or SMB -- neither can represent the hardlinks and atomic renames the importer depends on." -}}
{{- end -}}
{{- if and .Values.keda.enabled (not .Values.keda.prometheusAddress) -}}
{{- fail "keda.prometheusAddress must be set when keda.enabled=true: the JetStream lag triggers query prometheus-nats-exporter through the Prometheus scaler." -}}
{{- end -}}
{{- end -}}

{{/*
The shared workload shape: ServiceAccount + Service + Deployment.

Call with a dict:
  root       $
  component  "catalogarr"           -- also the ServiceAccount/Service name suffix
  image      "media" | "controller" -- key under .Values.image
  args       list of container args
  values     the per-service values block (enabled/replicas/resources/...)
  data       true to mount the RWX /data volume
  index      true to mount the RWO index volume at /var/lib/clustarr/index
  strategy   "Recreate" to pin the update strategy, "" for the default
  grace      terminationGracePeriodSeconds
  httpPort   true to expose the Torznab facade port
  extraEnv   list of extra env maps (optional)
  leaderElect true for a component that runs controllers (§3, §12)
*/}}
{{- define "clustarr.workload" }}
{{- $root := .root }}
{{- $name := printf "%s-%s" (include "clustarr.fullname" $root) .component }}
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ $name }}
  labels:
    {{- include "clustarr.labels" $root | nindent 4 }}
    app.kubernetes.io/component: {{ .component }}
{{- with $root.Values.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
---
apiVersion: v1
kind: Service
metadata:
  name: {{ $name }}
  labels:
    {{- include "clustarr.labels" $root | nindent 4 }}
    app.kubernetes.io/component: {{ .component }}
spec:
  type: {{ if .httpPort }}{{ .values.facade.service.type }}{{ else }}ClusterIP{{ end }}
  selector:
    {{- include "clustarr.selectorLabels" (dict "root" $root "component" .component) | nindent 4 }}
  ports:
  {{- if .httpPort }}
  - name: http
    port: {{ .values.facade.service.port }}
    targetPort: http
    protocol: TCP
  {{- end }}
  - name: metrics
    port: 8443
    targetPort: metrics
    protocol: TCP
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ $name }}
  labels:
    {{- include "clustarr.labels" $root | nindent 4 }}
    app.kubernetes.io/component: {{ .component }}
spec:
  {{- if and $root.Values.keda.enabled (eq .component "captionarr-worker") }}
  # replicas omitted: the ScaledObject owns it.
  {{- else }}
  replicas: {{ .values.replicas }}
  {{- end }}
  {{- with .strategy }}
  strategy:
    type: {{ . }}
  {{- end }}
  selector:
    matchLabels:
      {{- include "clustarr.selectorLabels" (dict "root" $root "component" .component) | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "clustarr.labels" $root | nindent 8 }}
        app.kubernetes.io/component: {{ .component }}
    spec:
      serviceAccountName: {{ $name }}
      terminationGracePeriodSeconds: {{ .grace }}
      {{- with $root.Values.imagePullSecrets }}
      imagePullSecrets:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{/*
        fsGroup/fsGroupChangePolicy are what make the kubelet chown a mounted
        volume to the pod's group. Every component that mounts a PVC needs
        them: a freshly provisioned PVC root is root:root 0755 on most block
        CSI drivers, and these pods run as uid 1000, so without the chown the
        first write fails and the pod crash-loops forever. The condition must
        therefore cover every flag that adds a persistentVolumeClaim volume
        below -- it read `if .data` alone, which left indexarr (the only
        component with "data" false and "index" true) with runAsUser: 1000 and
        no fsGroup, unable to create releases.db. The chart-only kustomize
        sets fsGroup on both. TestEveryPVCMountingWorkloadGetsFsGroup pins it.
      */}}
      securityContext:
        {{- if or .data .index }}
        {{- toYaml $root.Values.podSecurityContext | nindent 8 }}
        {{- else }}
        {{- omit $root.Values.podSecurityContext "fsGroup" "fsGroupChangePolicy" | toYaml | nindent 8 }}
        {{- end }}
      containers:
      - name: {{ .component }}
        image: {{ include "clustarr.image" (dict "root" $root "which" .image) }}
        imagePullPolicy: {{ $root.Values.image.pullPolicy }}
        {{- $args := .args }}
        {{- if .leaderElect }}
        {{- $args = append $args "--leader-elect" }}
        {{- end }}
        {{- if eq (include "clustarr.natsSingleNode" $root) "true" }}
        {{- $args = append $args "--nats-single-node" }}
        {{- end }}
        args:
          {{- toYaml $args | nindent 8 }}
        env:
        - name: NATS_URL
          value: {{ include "clustarr.natsUrl" $root | quote }}
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: POD_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        {{- if .data }}
        - name: UMASK
          value: "002"
        {{- end }}
        {{- with dig "resources" "limits" "memory" "" .values }}
        - name: GOMEMLIMIT
          value: {{ include "clustarr.gomemlimit" . | quote }}
        {{- end }}
        {{- with .extraEnv }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
        ports:
        {{- if .httpPort }}
        - name: http
          containerPort: 8080
          protocol: TCP
        {{- end }}
        - name: metrics
          containerPort: 8443
          protocol: TCP
        - name: health
          containerPort: 8081
          protocol: TCP
        livenessProbe:
          httpGet:
            path: /healthz
            port: health
          initialDelaySeconds: 15
          periodSeconds: 20
        readinessProbe:
          httpGet:
            path: /readyz
            port: health
          initialDelaySeconds: 5
          periodSeconds: 10
        resources:
          {{- toYaml .values.resources | nindent 10 }}
        securityContext:
          {{- toYaml $root.Values.securityContext | nindent 10 }}
        volumeMounts:
        {{- if .data }}
        - name: data
          mountPath: /data
        {{- end }}
        {{- if .index }}
        - name: index
          mountPath: /var/lib/clustarr/index
        {{- end }}
        - name: tmp
          mountPath: /tmp
      {{- with .values.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .values.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .values.affinity }}
      affinity:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      volumes:
      {{- if .data }}
      - name: data
        persistentVolumeClaim:
          claimName: {{ default (printf "%s-data" (include "clustarr.fullname" $root)) $root.Values.storage.data.existingClaim }}
      {{- end }}
      {{- if .index }}
      - name: index
        persistentVolumeClaim:
          claimName: {{ default (printf "%s-index" (include "clustarr.fullname" $root)) $root.Values.storage.index.existingClaim }}
      {{- end }}
      # readOnlyRootFilesystem: true, so /tmp has to be a volume.
      - name: tmp
        emptyDir: {}
{{- end }}
