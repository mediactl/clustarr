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
The Secret holding the Torznab facade's API keys (--facade-api-key-secret on
the manager and the index agent, which must name the same Secret). The
manager creates it with one random key when it is absent and never modifies
it; the index agent only reads it. The chart only names it -- it does not
render it -- so `helm uninstall` leaves the key behind for a reinstall to
reuse. Set agents.index.facade.apiKeySecret to use a Secret you manage.
*/}}
{{- define "clustarr.facadeAPIKeySecret" -}}
{{- default (printf "%s-index-facade" (include "clustarr.fullname" .)) .Values.agents.index.facade.apiKeySecret -}}
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
Selector labels for one component. Call with (dict "root" $ "component" "manager").
Immutable on a Deployment, so keep this to name/instance/component only --
adding the chart version here would break every upgrade.
*/}}
{{- define "clustarr.selectorLabels" -}}
app.kubernetes.io/name: {{ include "clustarr.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .component }}
{{- end -}}

{{/*
Image reference. Call with (dict "root" $ "which" "clustarr"|"native"), the
key under .Values.image.
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
GOMEMLIMIT: a share of a Kubernetes memory quantity, in whole bytes. Call
with (list <resources.limits.memory> <percent>): 80 for every Go-heavy
process, 60 for agent-import and agent-caption and 50 for markers, whose
FFmpeg, par2 and ONNX Runtime allocations live outside the Go heap (spec
2026-10-06 §10.2.1, OD42). The Downward API's resourceFieldRef on a
container's own limit only ever gives back 100% of it -- there is no scaling
operator -- so the share is computed once, here, from the same quantity
values.yaml puts in resources.limits.memory, and baked into the rendered env
var as a literal.

This covers every workload the chart renders but not the torrent/usenet
engine StatefulSets and Deployments the manager's DownloadClient controller
creates at runtime: those take their resources from
DownloadClient.spec.resources, a CRD field this chart never sees, so the same
calculation is made again in app/grab/controller/downloadclient (Go).

The quantity may be a bare byte count or carry a binary (Ki/Mi/Gi/Ti) or
decimal (K/M/G/T) suffix -- every form values.yaml uses and the ones
`kubectl` normally accepts for memory. Anything else (exponent form, a "m"
milli-suffix, which is meaningless for memory) fails the render rather than
silently emitting a wrong number. x pct / 100 rather than x 0.8: the product
of two integers is exact, so the chart and an integer check agree to the byte.
*/}}
{{- define "clustarr.gomemlimit" -}}
{{- $q := index . 0 -}}
{{- $pct := index . 1 -}}
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
{{- printf "%.0f" (divf (mulf ($num | float64) $mult $pct) 100.0) -}}
{{- end -}}

{{/*
CLUSTARR_CONSUMER_SLOTS from a consumerSlots map (consumer -> per-pod
slots): "a=1,b=2", keys in sorted order (spec 2026-10-06 §9.2). The
autoscale reconciler reads the same value from the pod template.
*/}}
{{- define "clustarr.consumerSlots" -}}
{{- $pairs := list -}}
{{- range $consumer, $slots := . -}}
{{- $pairs = append $pairs (printf "%s=%d" $consumer (int $slots)) -}}
{{- end -}}
{{- join "," $pairs -}}
{{- end -}}

{{/*
Guard rails. These are correctness invariants from the design spec, not
preferences, so they fail the render rather than warn.
*/}}
{{- define "clustarr.validate" -}}
{{- if and .Values.postgres.enabled (index .Values "cloudnative-pg" "enabled") .Release.IsInstall -}}
{{- fail "postgres.enabled and cloudnative-pg.enabled cannot both be true on a first `helm install`: CNPG's admission webhook fails closed until the operator this chart would install is actually Ready, and a first install submits every rendered object -- including the Cluster -- in one pass, before that operator can possibly be running yet. Install in two steps instead: `helm install ... --set cloudnative-pg.enabled=true --set postgres.enabled=false --wait` (waits for the bundled operator's own resources to be Ready), then `helm upgrade ... --set cloudnative-pg.enabled=true --set postgres.enabled=true` to add the Cluster once its webhook is up. If CloudNativePG is already running elsewhere in the cluster, leave cloudnative-pg.enabled=false and set only postgres.enabled=true -- there is no ordering problem to guard against then, just `kubectl wait --for=condition=Available deployment/cnpg-controller-manager -n cnpg-system` first." -}}
{{- end -}}
{{- if and (not .Values.storage.data.existingClaim) (ne .Values.storage.data.accessMode "ReadWriteMany") -}}
{{- fail "storage.data.accessMode must be ReadWriteMany: /data is one volume shared by the manager, the import and caption agents, markers, the download engines and the transcode pools, and import is a hardlink or rename inside that single filesystem. Use CephFS, or NFS/Longhorn RWX. Never exFAT or SMB -- neither can represent the hardlinks and atomic renames the importer depends on." -}}
{{- end -}}
{{- with .Values.manager.cardigann.definitions -}}
{{- if and .configMap .existingClaim -}}
{{- fail "manager.cardigann.definitions: set configMap or existingClaim, not both: the manager's bundle loader reads one Cardigann definitions directory (--cardigann-definitions-dir), mounted from exactly one volume." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
The shared workload shape: ServiceAccount + Service + Deployment.

Call with a dict:
  root               $
  component          "manager" | "agent-<domain>" -- also the ServiceAccount/Service name suffix
  image              "clustarr" | "native" -- key under .Values.image
  command            the binary (pkg/binpath); every container names one, no image has an ENTRYPOINT
  args               list of container args
  values             the workload's values block (resources, consumerSlots, scheduling)
  replicas           the fixed replica count
  autoscaled         true for a domain whose HPA the manager owns (pkg/agentdomain): with
                     autoscaling.enabled the Deployment carries autoscale.clustarr.io/domain
                     and no spec.replicas; a soft hostname spread applies either way
  domain             that label's value
  natsSingleNodeFlag append --nats-single-node when the JetStream topology is single-node
                     (the manager only: agents never ensure topology)
  leaderElect        append --leader-elect (the manager only)
  nodeName           true for an agent: NODE_NAME from spec.nodeName, for its presence
                     report (ADR-0019 §5.3)
  data               true to mount the RWX /data volume read-write, with UMASK 002
  index              true to mount the RWO index volume at /var/lib/clustarr/index
  strategy           a Deployment strategy map, or "" for the apps/v1 default
  grace              terminationGracePeriodSeconds (longest AckWait drained + 25 s, spec §3.5.6)
  gomemPercent       GOMEMLIMIT's share of resources.limits.memory (default 80)
  serviceType        the Service's type (default ClusterIP)
  extraPorts         list of (dict "name" "containerPort") before metrics and health
  extraServicePorts  list of (dict "name" "port" "targetPort") before metrics
  extraEnv / extraVolumeMounts / extraVolumes   lists, rendered as given
*/}}
{{- define "clustarr.workload" }}
{{- $root := .root }}
{{- $name := printf "%s-%s" (include "clustarr.fullname" $root) .component }}
{{- /* `and` stops at a false .autoscaled, so .Values.autoscaling is read
       only by a caller that asks for autoscaling. */}}
{{- $autoscaled := and .autoscaled $root.Values.autoscaling.enabled }}
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
  type: {{ default "ClusterIP" .serviceType }}
  selector:
    {{- include "clustarr.selectorLabels" (dict "root" $root "component" .component) | nindent 4 }}
  ports:
  {{- range .extraServicePorts }}
  - name: {{ .name }}
    port: {{ .port }}
    targetPort: {{ .targetPort }}
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
    {{- if $autoscaled }}
    autoscale.clustarr.io/domain: {{ .domain }}
    {{- end }}
spec:
  {{- if $autoscaled }}
  # replicas omitted: the manager's autoscale reconciler owns this
  # Deployment's HPA. Created without it, it starts at 1; an explicit 0 would
  # leave it paused and never woken (spec §10.2.2).
  {{- else }}
  replicas: {{ .replicas }}
  {{- end }}
  {{- with .strategy }}
  strategy:
    {{- toYaml . | nindent 4 }}
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
        below -- it read `if .data` alone, which left the release index's
        workload (the only one with "data" false and "index" true) with
        runAsUser: 1000 and no fsGroup, unable to create releases.db. The
        kustomize manifests set fsGroup on both.
        TestEveryPVCMountingWorkloadGetsFsGroup pins it.
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
        {{- with .command }}
        command:
        {{- toYaml (list .) | nindent 8 }}
        {{- end }}
        {{- $args := .args }}
        {{- if .leaderElect }}
        {{- $args = append $args "--leader-elect" }}
        {{- end }}
        {{- if and .natsSingleNodeFlag (eq (include "clustarr.natsSingleNode" $root) "true") }}
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
        {{- if .nodeName }}
        - name: NODE_NAME
          valueFrom:
            fieldRef:
              fieldPath: spec.nodeName
        {{- end }}
        {{- if .data }}
        - name: UMASK
          value: "002"
        {{- end }}
        {{- with dig "resources" "limits" "memory" "" .values }}
        - name: GOMEMLIMIT
          value: {{ include "clustarr.gomemlimit" (list . (default 80 $.gomemPercent)) | quote }}
        {{- end }}
        {{- with .values.consumerSlots }}
        - name: CLUSTARR_CONSUMER_SLOTS
          value: {{ include "clustarr.consumerSlots" . | quote }}
        {{- end }}
        {{- with .extraEnv }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
        ports:
        {{- range .extraPorts }}
        - name: {{ .name }}
          containerPort: {{ .containerPort }}
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
        {{- with .extraVolumeMounts }}
        {{- toYaml . | nindent 8 }}
        {{- end }}
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
      {{- if .autoscaled }}
      # Soft: a required anti-affinity would stall a RollingUpdate surge on a
      # single node (spec §10.2.1, OD33).
      topologySpreadConstraints:
      - maxSkew: 1
        topologyKey: kubernetes.io/hostname
        whenUnsatisfiable: ScheduleAnyway
        labelSelector:
          matchLabels:
            {{- include "clustarr.selectorLabels" (dict "root" $root "component" .component) | nindent 12 }}
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
      {{- with .extraVolumes }}
      {{- toYaml . | nindent 6 }}
      {{- end }}
{{- end }}
