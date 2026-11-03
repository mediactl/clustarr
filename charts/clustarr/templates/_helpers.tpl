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
{{/*
clustarr.managerPorts is the manager container's ports besides metrics and
health, as a JSON list: the External Metrics API's and the Live TV tuner's.
The Deployment and the Live TV NetworkPolicy both read it, so the policy
leaves every port the manager declares open (Live TV spec §5.3; held by
test/guards/livetv).
*/}}
{{- define "clustarr.managerPorts" -}}
{{- $ports := list }}
{{- if .Values.autoscaling.enabled }}
{{- $ports = append $ports (dict "name" "extmetrics" "containerPort" 6443) }}
{{- end }}
{{- if .Values.livetv.enabled }}
{{- $ports = append $ports (dict "name" "livetv" "containerPort" (int .Values.livetv.port)) }}
{{- end }}
{{- toJson $ports }}
{{- end }}

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
{{/*
clustarr.everyTermHas is "true" when affinity has a required node-affinity
term and every term holds every one of exprs: the check for a placement a
subchart takes literally (control-plane placement design §5.2, §5.5).
Call with (dict "affinity" <affinity map> "exprs" <list of expressions>).
*/}}
{{- define "clustarr.everyTermHas" -}}
{{- $terms := list -}}
{{- with .affinity -}}{{- with .nodeAffinity -}}{{- with .requiredDuringSchedulingIgnoredDuringExecution -}}
{{- $terms = .nodeSelectorTerms | default list -}}
{{- end -}}{{- end -}}{{- end -}}
{{- $ok := gt (len $terms) 0 -}}
{{- range $term := $terms -}}
{{- range $want := $.exprs -}}
{{- $found := false -}}
{{- range $have := ($term.matchExpressions | default list) -}}
{{- if and (eq $have.key $want.key) (eq $have.operator $want.operator) (eq (toJson ($have.values | default list)) (toJson ($want.values | default list))) -}}
{{- $found = true -}}
{{- end -}}
{{- end -}}
{{- if not $found -}}{{- $ok = false -}}{{- end -}}
{{- end -}}
{{- end -}}
{{- if $ok }}true{{ end -}}
{{- end -}}

{{- define "clustarr.validate" -}}
{{- if and (or .Values.postgres.enabled (index .Values "cluster-plex" "enabled")) (index .Values "cloudnative-pg" "enabled") .Release.IsInstall -}}
{{- fail "postgres.enabled or cluster-plex.enabled (whose database is a CloudNativePG Cluster too) cannot be set with cloudnative-pg.enabled on a first `helm install`: CNPG's admission webhook fails closed until the operator this chart would install is Ready. Install the operator first (cloudnative-pg.enabled=true alone, --wait), then upgrade with postgres.enabled or cluster-plex.enabled." -}}
{{- end -}}
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
{{- if and .Values.ui.enabled .Values.ui.autoscaling.enabled -}}
{{- $a := .Values.ui.autoscaling -}}
{{- if hasKey .Values.ui "replicas" -}}
{{- fail "ui.replicas and ui.autoscaling.enabled cannot both be set: the HorizontalPodAutoscaler owns the count between ui.autoscaling.minReplicas and maxReplicas, and the Deployment carries no spec.replicas." -}}
{{- end -}}
{{- if lt (int $a.minReplicas) 1 -}}
{{- fail "ui.autoscaling.minReplicas must be at least 1: a ui with no pod answers nothing, and the HPA scales on the pods' CPU." -}}
{{- end -}}
{{- if gt (int $a.minReplicas) (int $a.maxReplicas) -}}
{{- fail (printf "ui.autoscaling.minReplicas (%v) exceeds maxReplicas (%v)" $a.minReplicas $a.maxReplicas) -}}
{{- end -}}
{{- $p := include "clustarr.placementProfile" (dict "root" $ "values" .Values.ui) | fromJson -}}
{{- if and $p.onePerNode (gt (int $a.maxReplicas) (int $p.replicas)) -}}
{{- fail (printf "ui.autoscaling.maxReplicas (%v) exceeds placement profile %q's %v: the profile runs one pod per node, so a replica past its count would stay Pending for good" $a.maxReplicas .Values.ui.placement $p.replicas) -}}
{{- end -}}
{{- if not (dig "requests" "cpu" "" .Values.ui.resources) -}}
{{- fail "ui.autoscaling.enabled needs ui.resources.requests.cpu: the HPA targets CPU utilization, which is a share of the request." -}}
{{- end -}}
{{- end -}}
{{- if and .Values.ui.enabled .Values.ui.httpRoute.enabled (not .Values.ui.httpRoute.parentRefs) -}}
{{- fail "ui.httpRoute.enabled needs ui.httpRoute.parentRefs: the Gateway (name, namespace, and sectionName for one listener) the route attaches to; a route with no parent is attached to nothing." -}}
{{- end -}}
{{- if .Values.placement.enabled -}}
{{- $cp := .Values.placement.profiles.controlPlane.nodeAffinity -}}
{{- $workers := .Values.placement.profiles.workers.nodeAffinity -}}
{{- $nats := .Values.nats | default dict -}}
{{- if and $nats.enabled (dig "config" "cluster" "enabled" false $nats) -}}
{{- if ne (include "clustarr.everyTermHas" (dict "affinity" (dig "podTemplate" "merge" "spec" "affinity" dict $nats) "exprs" $cp)) "true" -}}
{{- fail "placement.enabled: nats.podTemplate.merge.spec.affinity must hold placement.profiles.controlPlane's nodeAffinity in every required term: the NATS servers run one per control-plane node (control-plane placement design §5.2), and the nats subchart takes its placement literally." -}}
{{- end -}}
{{- end -}}
{{- if and $nats.enabled (dig "natsBox" "enabled" true $nats) -}}
{{- if ne (include "clustarr.everyTermHas" (dict "affinity" (dig "natsBox" "podTemplate" "merge" "spec" "affinity" dict $nats) "exprs" $workers)) "true" -}}
{{- fail "placement.enabled: nats.natsBox.podTemplate.merge.spec.affinity must hold placement.profiles.workers' nodeAffinity in every required term: nats-box runs on the workers with everything else (control-plane placement design §5.5)." -}}
{{- end -}}
{{- end -}}
{{- if and (index .Values "cluster-plex" "enabled") (dig "proxy" "enabled" false (index .Values "cluster-plex")) -}}
{{- if ne (include "clustarr.everyTermHas" (dict "affinity" (dig "proxy" "affinity" dict (index .Values "cluster-plex")) "exprs" $cp)) "true" -}}
{{- fail "placement.enabled: cluster-plex.proxy.affinity must hold placement.profiles.controlPlane's nodeAffinity in every required term: the proxy has its own LoadBalancer Service, and a workload behind a LoadBalancer Service runs on the control plane, where the BGP peers are (owner, 2026-10-21; control-plane placement design §5.5)." -}}
{{- end -}}
{{- end -}}
{{- if index .Values "cluster-plex" "enabled" -}}
{{- if ne (include "clustarr.everyTermHas" (dict "affinity" (dig "plex" "affinity" dict (index .Values "cluster-plex")) "exprs" $cp)) "true" -}}
{{- fail "placement.enabled: cluster-plex.plex.affinity must hold placement.profiles.controlPlane's nodeAffinity in every required term: a workload behind a LoadBalancer Service runs on the control plane, where the BGP peers are (owner, 2026-10-21; control-plane placement design §5.5)." -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Placement (docs/superpowers/specs/2026-10-21-control-plane-placement-design.md
§3). clustarr.placementProfile is the profile a component's values name, as
JSON: {} with placement off or when the component names none.
Call with (dict "root" $ "values" <component values>).
*/}}
{{- define "clustarr.placementProfile" -}}
{{- $p := dict -}}
{{- if .root.Values.placement.enabled -}}
{{- with .values.placement -}}
{{- $p = index $.root.Values.placement.profiles . | default dict -}}
{{- if not $p -}}
{{- fail (printf "placement %q names no profile under placement.profiles" .) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- toJson $p -}}
{{- end -}}

{{/*
clustarr.placement renders a pod's nodeSelector, tolerations and affinity:
the component's own, with the profile's expressions joined to every required
node-affinity term (terms OR, so a new term would let the pod skip them),
the profile's tolerations added, and, for a onePerNode profile, a required
pod anti-affinity on kubernetes.io/hostname over the component's own pods.
With placement off it is exactly the component's own three keys.
Call with (dict "root" $ "values" <component values> "component" <name>),
included at the pod spec's indentation with nindent.
*/}}
{{- define "clustarr.placement" -}}
{{- $p := include "clustarr.placementProfile" . | fromJson -}}
{{- $affinity := deepCopy (.values.affinity | default dict) -}}
{{- if $p.nodeAffinity -}}
{{- $na := $affinity.nodeAffinity | default dict -}}
{{- $req := $na.requiredDuringSchedulingIgnoredDuringExecution | default dict -}}
{{- $terms := list -}}
{{- range ($req.nodeSelectorTerms | default (list (dict))) -}}
{{- $t := deepCopy . -}}
{{- $_ := set $t "matchExpressions" (concat (.matchExpressions | default list) $p.nodeAffinity) -}}
{{- $terms = append $terms $t -}}
{{- end -}}
{{- $_ := set $req "nodeSelectorTerms" $terms -}}
{{- $_ := set $na "requiredDuringSchedulingIgnoredDuringExecution" $req -}}
{{- $_ := set $affinity "nodeAffinity" $na -}}
{{- end -}}
{{- if $p.onePerNode -}}
{{- $paa := $affinity.podAntiAffinity | default dict -}}
{{- $sel := include "clustarr.selectorLabels" (dict "root" .root "component" .component) | fromYaml -}}
{{- $anti := append ($paa.requiredDuringSchedulingIgnoredDuringExecution | default list) (dict "topologyKey" "kubernetes.io/hostname" "labelSelector" (dict "matchLabels" $sel)) -}}
{{- $_ := set $paa "requiredDuringSchedulingIgnoredDuringExecution" $anti -}}
{{- $_ := set $affinity "podAntiAffinity" $paa -}}
{{- end -}}
{{- $tolerations := concat (.values.tolerations | default list) ($p.tolerations | default list) -}}
{{- with .values.nodeSelector }}
nodeSelector:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with $tolerations }}
tolerations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with $affinity }}
affinity:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{/*
clustarr.placementReplicas is a Deployment's replicas: the component's own
when set, else a onePerNode profile's, else .default. More than a onePerNode
profile allows fails the render: the extra pod would stay Pending for good.
Call with (dict "root" $ "values" <component values> "component" <values key> "default" <int>).
*/}}
{{- define "clustarr.placementReplicas" -}}
{{- $p := include "clustarr.placementProfile" . | fromJson -}}
{{- $n := .default -}}
{{- if $p.onePerNode -}}{{- $n = $p.replicas -}}{{- end -}}
{{- if hasKey .values "replicas" -}}
{{- if and $p.onePerNode (gt (int .values.replicas) (int $p.replicas)) -}}
{{- fail (printf "%s.replicas (%v) exceeds placement profile %q's %v: the profile runs one pod per node" .component .values.replicas .values.placement $p.replicas) -}}
{{- end -}}
{{- $n = .values.replicas -}}
{{- end -}}
{{- int $n -}}
{{- end -}}

{{/*
clustarr.onePerNode is "true" for a component whose profile runs one pod per
node, else "": its Deployment rolls with maxSurge 0, since with exactly one
eligible node per pod a surge pod has nowhere to go.
Call with (dict "root" $ "values" <component values>).
*/}}
{{- define "clustarr.onePerNode" -}}
{{- $p := include "clustarr.placementProfile" . | fromJson -}}
{{- if $p.onePerNode }}true{{ end -}}
{{- end -}}

{{/*
clustarr.cnpgAffinity is a CloudNativePG Cluster's spec.affinity from its
placement profile: the profile's expressions as a required node affinity, its
tolerations, and for a onePerNode profile a required anti-affinity on
kubernetes.io/hostname. Empty with placement off.
Call with (dict "root" $ "values" <the Cluster's values>).
*/}}
{{- define "clustarr.cnpgAffinity" -}}
{{- $p := include "clustarr.placementProfile" . | fromJson -}}
{{- $a := dict -}}
{{- if $p.nodeAffinity -}}
{{- if $p.onePerNode -}}
{{- $_ := set $a "enablePodAntiAffinity" true -}}
{{- $_ := set $a "podAntiAffinityType" "required" -}}
{{- $_ := set $a "topologyKey" "kubernetes.io/hostname" -}}
{{- end -}}
{{- $_ := set $a "nodeAffinity" (dict "requiredDuringSchedulingIgnoredDuringExecution" (dict "nodeSelectorTerms" (list (dict "matchExpressions" $p.nodeAffinity)))) -}}
{{- with $p.tolerations -}}{{- $_ := set $a "tolerations" . -}}{{- end -}}
{{- end -}}
{{- /* The Cluster's own affinity (CNPG's AffinityConfiguration, verbatim)
       is laid over the profile's, key by key, so a Cluster is placed without
       placement.enabled, or past its profile. */ -}}
{{- $a = merge (deepCopy (.values.affinity | default dict)) $a -}}
{{- with $a }}
affinity:
  {{- toYaml . | nindent 2 }}
{{- end }}
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
  automemlimit       AUTOMEMLIMIT, the share of resources.limits.memory the binary
                     takes from its cgroup as its Go memory limit, for a process
                     whose FFmpeg or par2 allocates outside the Go heap ("0.60",
                     OD42); unset, automemlimit's own 0.9
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
        {{- /* Every caller is the manager or an agent, which bind
               --metrics-secure; this template owns the metrics port below. */}}
        {{- if not $root.Values.metrics.secure }}
        {{- $args = append $args "--metrics-secure=false" }}
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
        {{- if .leaderElect }}
        {{- /* The leader publishes this address as the livetv Service's
               endpoint (control-plane placement design §5.4). */}}
        - name: POD_IP
          valueFrom:
            fieldRef:
              fieldPath: status.podIP
        {{- end }}
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
        {{- /* Every binary takes a share of its cgroup's memory limit as its Go
               memory limit (github.com/KimMachineGun/automemlimit): 0.9, or
               .automemlimit's. A workload's own goMemLimit (Go's syntax,
               e.g. 1GiB) wins over it; gogc sets GOGC (control-plane
               placement design §5.4.1). */}}
        {{- if .values.goMemLimit }}
        - name: GOMEMLIMIT
          value: {{ .values.goMemLimit | quote }}
        {{- else if .automemlimit }}
        - name: AUTOMEMLIMIT
          value: {{ .automemlimit | quote }}
        {{- end }}
        {{- with .values.gogc }}
        - name: GOGC
          value: {{ . | quote }}
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
      {{- with (include "clustarr.placement" (dict "root" $root "values" .values "component" .component) | trim) }}
      {{- . | nindent 6 }}
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
