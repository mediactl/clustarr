/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package downloadclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	appsv1ac "k8s.io/client-go/applyconfigurations/apps/v1"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

// Labels the engine workload and its pods carry. labelClient deliberately
// reuses download.clustarr.io as its prefix, the same namespace
// downloadv1alpha1.LabelClient uses on Download objects, so a future
// "kubectl get pods -l download.clustarr.io/client=X" reads the same as the
// Download side.
const (
	labelComponent  = "app.kubernetes.io/component"
	labelClient     = "download.clustarr.io/client"
	componentEngine = "grabarr-engine"

	// dataVolumeName, scratchVolumeName and tmpVolumeName are the pod-local
	// volume names; they say nothing about the PVC or emptyDir backing them.
	dataVolumeName    = "data"
	scratchVolumeName = "scratch"
	tmpVolumeName     = "tmp"

	// tmpDir is an emptyDir because the root filesystem is read-only (see
	// containerSecurityContextAC): nothing in either engine writes there
	// today -- anacrolix keeps its piece-completion database beside the data
	// under /data, and the usenet engine's part files, par2 repair and
	// unpack all work under its scratch dir and /data -- but SQLite (the
	// cgo build's piece completion) and anything calling os.TempDir fall
	// back to /tmp, and an unwritable /tmp fails at runtime, on a real node,
	// where no test sees it. Every Deployment mounts one for the same reason.
	tmpDir = "/tmp"

	// podUID and podGID are the identity every engine pod runs as: the
	// clustarr user images/Dockerfile.media creates (USER 1000:1000), and the
	// runAsUser/runAsGroup/fsGroup every Deployment under config/manager
	// sets, grabarr's own included -- so an engine's files on /data belong
	// to the same group every other service reads and rewrites them as
	// (§11). TestEnginePodSecurityMatchesTheDeployment holds the engine pods
	// to config/manager/grabarr.yaml.
	podUID = int64(1000)
	podGID = int64(1000)

	// engineContainerName is the one container every engine pod runs.
	engineContainerName = "engine"

	// clustarrBinary is images/Dockerfile.media's ENTRYPOINT (verified: both
	// Dockerfile.media and Dockerfile.media-cuda set
	// `ENTRYPOINT ["/usr/local/bin/clustarr"]`), needed only by the torrent
	// container's shell wrapper -- every other container in the tree omits
	// `command` entirely and lets the image's own entrypoint run, matching
	// config/manager/grabarr.yaml's `args: ["grabarr", "--role", ...]`.
	clustarrBinary = "/usr/local/bin/clustarr"

	// defaultScratchSize backs an emptyDir when UsenetSpec.Scratch is unset;
	// ScratchSpec.SizeLimit's own kubebuilder default is "50Gi", restated here
	// because that default fills only an ABSENT field: a DownloadClient
	// created by a Go client always carries sizeLimit "0", so the apiserver
	// never defaults it, and neither does an in-memory object in a unit test.
	defaultScratchSize = "50Gi"

	// defaultListenPort mirrors TorrentSpec.ListenPort's kubebuilder default,
	// for the same reason.
	defaultListenPort = 42069
)

// EngineRuntime is what every engine pod needs from the controller's own
// process, beyond what its DownloadClient says, to run at all on a real
// cluster. Every field was missing until X14, and none of the gaps was
// visible to a test: envtest schedules no pods.
//
//   - ServiceAccountName: the engine reads its DownloadClient and watches,
//     updates and finalizes its Downloads. With no serviceAccountName the pod
//     ran as the namespace's "default" account, which nothing binds, and was
//     denied every one of those calls. Both installers create this account
//     and bind it to the engine's own generated ClusterRole
//     (config/rbac/grabarr_engine_role.yaml, from app/grab/engine's markers).
//   - NATSURL: the engine publishes download events and progress on the bus,
//     and its --nats-url default names a Service the installers do not
//     create; the controller hands on its own $NATS_URL.
//   - BusSingleNode: the engine ensures the same JetStream topology every
//     other service does, and on single-node NATS it must collapse replicas
//     as they do (--nats-single-node).
//   - Umask: design §11's UMASK 002, for what a torrent engine writes under
//     /data -- the same pass-through squasharr gives its transcode Jobs.
//
// POD_NAMESPACE needs no field: it is the downward API, set on every engine
// container, and it is how an engine finds its DownloadClient at all.
type EngineRuntime struct {
	ServiceAccountName string
	NATSURL            string
	BusSingleNode      bool
	Umask              string
}

// DefaultEngineServiceAccount is the ServiceAccount config/ creates for the
// engine pods (config/manager/grabarr.yaml) and binds to the engine role.
// The chart's is "<release fullname>-grabarr-engine", which grabarr's
// --engine-service-account ($CLUSTARR_ENGINE_SERVICE_ACCOUNT) carries.
const DefaultEngineServiceAccount = "grabarr-engine"

// gomemlimitFraction is design §12's "GOMEMLIMIT = 80 % of limit" for the
// torrent engines: the Downward API can only give the whole limit, so the
// controller computes the soft limit itself, as the chart's
// clustarr.gomemlimit helper does for the Deployments it renders.
const gomemlimitFraction = 0.8

// engineEnv is every engine container's environment: POD_NAMESPACE always,
// then whatever of rt the controller has, then GOMEMLIMIT when the
// DownloadClient sets a memory limit. GOMEMLIMIT is rendered exactly as the
// chart's helper renders it -- 80% of the limit in bytes, "%.0f" -- so a
// Deployment's and an engine's soft limit read as one convention.
func engineEnv(dc *downloadv1alpha1.DownloadClient, rt EngineRuntime) []*corev1ac.EnvVarApplyConfiguration {
	env := []*corev1ac.EnvVarApplyConfiguration{
		corev1ac.EnvVar().WithName("POD_NAMESPACE").WithValueFrom(corev1ac.EnvVarSource().
			WithFieldRef(corev1ac.ObjectFieldSelector().WithFieldPath("metadata.namespace"))),
	}
	if rt.NATSURL != "" {
		env = append(env, corev1ac.EnvVar().WithName("NATS_URL").WithValue(rt.NATSURL))
	}
	if rt.Umask != "" {
		env = append(env, corev1ac.EnvVar().WithName("UMASK").WithValue(rt.Umask))
	}
	if limit, ok := dc.Spec.Resources.Limits[corev1.ResourceMemory]; ok && !limit.IsZero() {
		soft := strconv.FormatFloat(float64(limit.Value())*gomemlimitFraction, 'f', 0, 64)
		env = append(env, corev1ac.EnvVar().WithName("GOMEMLIMIT").WithValue(soft))
	}
	return env
}

// engineWorkloadName is spec §6.3's `<downloadclient>-engine` naming: the
// StatefulSet (torrent) or Deployment (usenet) a DownloadClient owns.
//
// It does not guard against the 63-character DNS label limit a StatefulSet's
// pod names ("<name>-<ordinal>") must fit. Kubernetes' own object-name
// validation already allows names longer than that for the DownloadClient
// itself, so a StatefulSet whose name plus "-engine-<ordinal>" exceeds 63
// characters is rejected by the apiserver at apply time, surfacing as a
// reconcile error rather than something this function catches early. Adding
// truncation would need a collision-safe scheme (a hash suffix, as
// k8s.HashSuffix uses elsewhere) and is not exercised by anything in scope for
// this task.
func engineWorkloadName(clientName string) string {
	return clientName + "-engine"
}

// scratchClaimName is the companion PersistentVolumeClaim a usenet client
// gets when UsenetSpec.Scratch.StorageClassName is set.
func scratchClaimName(clientName string) string {
	return clientName + "-scratch"
}

// selectorLabels is both the StatefulSet/Deployment selector and the pod
// template's labels -- Kubernetes requires the two to match, and computing
// them once keeps that true by construction rather than by two call sites
// agreeing.
func selectorLabels(dc *downloadv1alpha1.DownloadClient) map[string]string {
	return map[string]string{
		labelComponent: componentEngine,
		labelClient:    dc.Name,
	}
}

// torrentReplicas is spec.replicas, floored at 1. DownloadClientSpec.Replicas
// carries a kubebuilder:default=1 the apiserver's structural defaulting
// applies on every object that reaches it through the API -- but a unit test
// building a DownloadClient in memory, never Created against an apiserver,
// sees the Go zero value instead, so the floor is restated here rather than
// trusted to have already happened.
func torrentReplicas(dc *downloadv1alpha1.DownloadClient) int32 {
	if dc.Spec.Replicas < 1 {
		return 1
	}
	return dc.Spec.Replicas
}

// resourceRequirementsAC renders a native corev1.ResourceRequirements (the
// type DownloadClientSpec.Resources actually is -- there is no apply
// configuration for it to begin with) as the apply configuration a container
// needs. Limits and Requests are the same corev1.ResourceList in both the
// native and apply-configuration shape, so this is a direct carry, not a
// field-by-field conversion; each is sent only when non-empty so a client
// with no resources set gets a container with no resources block, rather than
// one with two empty maps.
func resourceRequirementsAC(rr corev1.ResourceRequirements) *corev1ac.ResourceRequirementsApplyConfiguration {
	ac := corev1ac.ResourceRequirements()
	if len(rr.Requests) > 0 {
		ac = ac.WithRequests(rr.Requests)
	}
	if len(rr.Limits) > 0 {
		ac = ac.WithLimits(rr.Limits)
	}
	return ac
}

// tolerationsAC converts DownloadClientSpec.Tolerations, a plain
// []corev1.Toleration, into the apply-configuration form field by field; the
// two types share no representation the way ResourceList does.
func tolerationsAC(ts []corev1.Toleration) []*corev1ac.TolerationApplyConfiguration {
	out := make([]*corev1ac.TolerationApplyConfiguration, 0, len(ts))
	for _, t := range ts {
		ac := corev1ac.Toleration()
		if t.Key != "" {
			ac = ac.WithKey(t.Key)
		}
		if t.Operator != "" {
			ac = ac.WithOperator(t.Operator)
		}
		if t.Value != "" {
			ac = ac.WithValue(t.Value)
		}
		if t.Effect != "" {
			ac = ac.WithEffect(t.Effect)
		}
		if t.TolerationSeconds != nil {
			ac = ac.WithTolerationSeconds(*t.TolerationSeconds)
		}
		out = append(out, ac)
	}
	return out
}

// podSecurityContextAC is the pod half of the security settings every
// Deployment under config/manager carries, and squasharr's transcode Jobs
// with them: non-root as the image's clustarr user, the RuntimeDefault
// seccomp profile, and fsGroup on the RWX /data volume (and the usenet
// scratch claim) with OnRootMismatch, so a large library is not re-chowned
// on every engine start. Until X16 an engine pod had none of it: it ran as
// whatever the image said, unconfined, with no fsGroup on the volume its
// downloads land on.
func podSecurityContextAC() *corev1ac.PodSecurityContextApplyConfiguration {
	return corev1ac.PodSecurityContext().
		WithRunAsNonRoot(true).
		WithRunAsUser(podUID).
		WithRunAsGroup(podGID).
		WithFSGroup(podGID).
		WithFSGroupChangePolicy(corev1.FSGroupChangeOnRootMismatch).
		WithSeccompProfile(corev1ac.SeccompProfile().WithType(corev1.SeccompProfileTypeRuntimeDefault))
}

// containerSecurityContextAC is the container half: no privilege
// escalation, every capability dropped, and a read-only root filesystem.
// Each engine writes only to volumes -- /data (the transfers, the torrent
// engine's re-attach state and piece completion), the usenet engine's
// scratch dir, and /tmp -- and the torrent engine's peer port is the
// DownloadClient's listenPort, 42069 by default, unprivileged, so dropping
// NET_BIND_SERVICE with the rest costs nothing unless an operator picks a
// port below 1024.
func containerSecurityContextAC() *corev1ac.SecurityContextApplyConfiguration {
	return corev1ac.SecurityContext().
		WithAllowPrivilegeEscalation(false).
		WithReadOnlyRootFilesystem(true).
		WithCapabilities(corev1ac.Capabilities().WithDrop("ALL"))
}

// podSpecAC assembles the PodSpec every engine pod shares: the one container
// callers pass in, the shared /data volume every grabarr pod mounts (§6.3;
// config/manager/grabarr.yaml mounts the same "clustarr-data" claim on the
// controller Deployment), whatever extra volumes the caller needs (usenet's
// scratch volume), the /tmp emptyDir the read-only root filesystem needs,
// the pod security context, spec.nodeSelector and spec.tolerations.
func podSpecAC(dc *downloadv1alpha1.DownloadClient, container *corev1ac.ContainerApplyConfiguration, dataClaimName string, rt EngineRuntime, extraVolumes ...*corev1ac.VolumeApplyConfiguration) *corev1ac.PodSpecApplyConfiguration {
	volumes := append([]*corev1ac.VolumeApplyConfiguration{
		corev1ac.Volume().WithName(dataVolumeName).WithPersistentVolumeClaim(
			corev1ac.PersistentVolumeClaimVolumeSource().WithClaimName(dataClaimName)),
	}, extraVolumes...)
	volumes = append(volumes, corev1ac.Volume().WithName(tmpVolumeName).WithEmptyDir(corev1ac.EmptyDirVolumeSource()))

	spec := corev1ac.PodSpec().
		WithSecurityContext(podSecurityContextAC()).
		WithContainers(container).
		WithVolumes(volumes...).
		WithRestartPolicy(corev1.RestartPolicyAlways)
	if rt.ServiceAccountName != "" {
		spec = spec.WithServiceAccountName(rt.ServiceAccountName)
	}
	if len(dc.Spec.NodeSelector) > 0 {
		spec = spec.WithNodeSelector(dc.Spec.NodeSelector)
	}
	if tols := tolerationsAC(dc.Spec.Tolerations); len(tols) > 0 {
		spec = spec.WithTolerations(tols...)
	}
	return spec
}

func podTemplateAC(configHash string, labels map[string]string, spec *corev1ac.PodSpecApplyConfiguration) *corev1ac.PodTemplateSpecApplyConfiguration {
	return corev1ac.PodTemplateSpec().
		WithLabels(labels).
		WithAnnotations(map[string]string{EngineConfigHashAnnotation: configHash}).
		WithSpec(spec)
}

// EngineConfigHashAnnotation is the engine pod template's annotation holding
// [engineConfigHash]: a hash of every DownloadClient setting an engine reads
// once, at start. Changing one of those settings changes the pod template,
// so the StatefulSet or Deployment rolls its pods and each engine starts
// again with the new value -- the Deployment-annotation idiom for "restart
// on a config change". Before it, stallTimeout, downloadTimeout, the usenet
// providers and the rest took effect only when an engine happened to restart.
const EngineConfigHashAnnotation = "download.clustarr.io/engine-config-hash"

// engineConfigHash hashes what the engine for dc reads at start
// ([engineStartConfig]), with secrets the digest of each Secret it reads
// ([Reconciler.secretDigests]; nil for a torrent engine, which reads none).
// JSON is a stable encoding here: struct fields render in declaration order
// and map keys sorted.
func engineConfigHash(dc *downloadv1alpha1.DownloadClient, secrets map[string]string) string {
	raw, err := json.Marshal(engineStartConfig(dc, secrets))
	if err != nil {
		// Every type in it is a plain API type, which always marshals.
		panic(fmt.Sprintf("downloadclient: marshal engine config: %v", err))
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// engineStartConfig is what the engine for dc reads once, at start
// (app/grab/run.go's setupTorrentEngine and app/grab/engine/usenet.BuildClient):
//
//   - torrent: spec.torrent, less seed and removeCompleted. The engine reads
//     those two on every reconcile, so a change to them applies without a
//     restart, and restarting every ordinal for one would cost each torrent
//     its peers for nothing. Everything else in TorrentSpec is start-time --
//     listenPort, enableDHT, stallTimeout -- or not read yet, and a field
//     added later rolls the engine until someone decides otherwise, which is
//     the safe default.
//   - usenet: all of spec.usenet (providers, post-processing, pre-check, the
//     health floor and action, propagationDelay, downloadTimeout) and
//     spec.categories, which the usenet engine resolves once at construction.
//
// The usenet providers' Secrets are read at start too, so their data is in
// the hash as well, one digest per Secret: a rotated password restarts the
// engine like any spec change, at the next reconcile (Z1 follow-up; see
// [Reconciler.secretDigests] for how they are read, and why they are not
// watched).
//
// It deliberately leaves out what already changes the pod template on its
// own (resources, nodeSelector, tolerations) and what no engine reads
// (enabled, priority, replicas).
func engineStartConfig(dc *downloadv1alpha1.DownloadClient, secrets map[string]string) any {
	switch {
	case dc.Spec.Torrent != nil:
		t := dc.Spec.Torrent.DeepCopy()
		t.Seed = nil
		t.RemoveCompleted = nil
		return struct {
			Torrent *downloadv1alpha1.TorrentSpec `json:"torrent"`
		}{t}
	case dc.Spec.Usenet != nil:
		return struct {
			Usenet     *downloadv1alpha1.UsenetSpec `json:"usenet"`
			Categories map[string]string            `json:"categories,omitempty"`
			Secrets    map[string]string            `json:"secrets,omitempty"`
		}{dc.Spec.Usenet, dc.Spec.Categories, secrets}
	default:
		return struct{}{}
	}
}

// torrentContainer builds the torrent engine's container.
//
// The ordinal a StatefulSet pod gets is only knowable at the pod's own
// runtime, from its own hostname ("<workload>-<ordinal>", set by the
// StatefulSet controller, not by this reconciler): engineWorkloadName already
// bakes "-engine" into the workload name, so the pod name is
// "<client>-engine-<ordinal>" and grabarr.Options.Engine wants
// "<client>-<ordinal>" (see app/grab/run.go's doc comment on Engine). Getting
// from one to the other needs a shell, because Kubernetes has no field
// selector or downward-API projection that strips a hostname's ordinal
// suffix -- "${HOSTNAME##*-}" is POSIX parameter expansion for "everything
// after the last '-'", which is exactly the ordinal regardless of how many
// hyphens the client's own name contains. Every other container in this tree
// omits `command` and lets the image's entrypoint run; this is the one
// exception, and it exists for this reason alone.
//
// This is this task's invention, not a verified contract: D2-5, which writes
// the torrent engine's actual entrypoint handling, may find a cleaner
// mechanism (a downward-API env var the engine parses itself, for instance)
// and should feel free to change this rather than treat it as load-bearing.
// What IS load-bearing is the shape grabarr.Options.Engine documents:
// "<client>-<ordinal>".
func torrentContainer(dc *downloadv1alpha1.DownloadClient, image, dataDir string, rt EngineRuntime) *corev1ac.ContainerApplyConfiguration {
	// DownloadClientSpec's CEL rule guarantees spec.torrent is set whenever
	// protocol==torrent for any object that reached the apiserver, but this
	// still guards the nil case rather than dereferencing it directly: a unit
	// test builds a DownloadClient in memory, where CEL never runs.
	listenPort := int32(defaultListenPort)
	if dc.Spec.Torrent != nil && dc.Spec.Torrent.ListenPort != 0 {
		listenPort = dc.Spec.Torrent.ListenPort
	}
	script := fmt.Sprintf(
		`ordinal=${HOSTNAME##*-}; exec %s grabarr --role torrent-engine --data-dir %s --engine %s-${ordinal}`,
		clustarrBinary, dataDir, dc.Name)
	if rt.BusSingleNode {
		script += " --nats-single-node"
	}

	return corev1ac.Container().
		WithName(engineContainerName).
		WithImage(image).
		WithCommand("/bin/sh", "-c").
		WithArgs(script).
		WithPorts(corev1ac.ContainerPort().
			WithName("peer").
			WithContainerPort(listenPort).
			WithProtocol(corev1.ProtocolTCP)).
		WithEnv(engineEnv(dc, rt)...).
		WithResources(resourceRequirementsAC(dc.Spec.Resources)).
		WithSecurityContext(containerSecurityContextAC()).
		WithVolumeMounts(
			corev1ac.VolumeMount().WithName(dataVolumeName).WithMountPath(dataDir),
			corev1ac.VolumeMount().WithName(tmpVolumeName).WithMountPath(tmpDir),
		)
}

// usenetContainer builds the usenet engine's container. Usenet clients are
// capped at spec.replicas==1 by DownloadClientSpec's own CEL rule, so the
// engine identity is always ordinal 0 -- no shell, no hostname parsing, just
// the same "grabarr --role ..." argv config/manager/grabarr.yaml already uses
// for the controller.
func usenetContainer(dc *downloadv1alpha1.DownloadClient, image, dataDir, scratchDir string, rt EngineRuntime) *corev1ac.ContainerApplyConfiguration {
	args := []string{
		"grabarr",
		"--role", "usenet-engine",
		"--data-dir", dataDir,
		"--scratch-dir", scratchDir,
		"--engine", dc.Name + "-0",
	}
	if rt.BusSingleNode {
		args = append(args, "--nats-single-node")
	}
	return corev1ac.Container().
		WithName(engineContainerName).
		WithImage(image).
		WithArgs(args...).
		WithEnv(engineEnv(dc, rt)...).
		WithResources(resourceRequirementsAC(dc.Spec.Resources)).
		WithSecurityContext(containerSecurityContextAC()).
		WithVolumeMounts(
			corev1ac.VolumeMount().WithName(dataVolumeName).WithMountPath(dataDir),
			corev1ac.VolumeMount().WithName(scratchVolumeName).WithMountPath(scratchDir),
			corev1ac.VolumeMount().WithName(tmpVolumeName).WithMountPath(tmpDir),
		)
}

// scratchSize is UsenetSpec.Scratch.SizeLimit when set, else
// [defaultScratchSize] -- restated for the same in-memory-object reason
// [torrentReplicas] restates the replicas floor.
func scratchSize(dc *downloadv1alpha1.DownloadClient) resource.Quantity {
	if dc.Spec.Usenet != nil && dc.Spec.Usenet.Scratch != nil && !dc.Spec.Usenet.Scratch.SizeLimit.IsZero() {
		return dc.Spec.Usenet.Scratch.SizeLimit
	}
	return resource.MustParse(defaultScratchSize)
}

// scratchVolume returns the usenet pod's scratch volume: a PersistentVolumeClaim
// when UsenetSpec.Scratch.StorageClassName is set (the caller is responsible
// for having applied that claim -- see buildScratchPVC), or an emptyDir sized
// from scratchSize otherwise. ScratchSpec's own doc comment is explicit that
// "nil = emptyDir backed by node storage", so this branches on nil-ness, not
// on any other signal.
func scratchVolume(dc *downloadv1alpha1.DownloadClient) *corev1ac.VolumeApplyConfiguration {
	if dc.Spec.Usenet != nil && dc.Spec.Usenet.Scratch != nil && dc.Spec.Usenet.Scratch.StorageClassName != nil {
		return corev1ac.Volume().WithName(scratchVolumeName).WithPersistentVolumeClaim(
			corev1ac.PersistentVolumeClaimVolumeSource().WithClaimName(scratchClaimName(dc.Name)))
	}
	size := scratchSize(dc)
	return corev1ac.Volume().WithName(scratchVolumeName).WithEmptyDir(
		corev1ac.EmptyDirVolumeSource().WithSizeLimit(size))
}

// buildScratchPVC renders the companion PersistentVolumeClaim a usenet client
// needs when it names a StorageClass. It is applied (via [k8s.Apply]) before
// the Deployment that references it, same as [buildStatefulSet] needs no PVC
// at all because torrent engines write straight into the shared /data volume.
func buildScratchPVC(dc *downloadv1alpha1.DownloadClient, owner *metav1ac.OwnerReferenceApplyConfiguration) *corev1ac.PersistentVolumeClaimApplyConfiguration {
	size := scratchSize(dc)
	spec := corev1ac.PersistentVolumeClaimSpec().
		WithAccessModes(corev1.ReadWriteOnce).
		WithResources(corev1ac.VolumeResourceRequirements().WithRequests(corev1.ResourceList{
			corev1.ResourceStorage: size,
		}))
	if dc.Spec.Usenet != nil && dc.Spec.Usenet.Scratch != nil && dc.Spec.Usenet.Scratch.StorageClassName != nil {
		spec = spec.WithStorageClassName(*dc.Spec.Usenet.Scratch.StorageClassName)
	}
	return corev1ac.PersistentVolumeClaim(scratchClaimName(dc.Name), dc.Namespace).
		WithLabels(selectorLabels(dc)).
		WithOwnerReferences(owner).
		WithSpec(spec)
}

// buildStatefulSet renders the torrent engine's StatefulSet: spec.replicas
// ordinals (floored at 1), each mounting the shared /data volume, running
// [torrentContainer]. It carries no volumeClaimTemplates -- spec §6.3's torrent
// engine pod persists its re-attach state under /data/torrents/.state on the
// shared volume, not on a per-ordinal PVC, so the StatefulSet's only reason to
// exist is the stable "<workload>-<ordinal>" pod identity re-attach depends
// on, not per-pod storage.
func buildStatefulSet(dc *downloadv1alpha1.DownloadClient, name, image, dataDir, dataClaimName string, rt EngineRuntime, owner *metav1ac.OwnerReferenceApplyConfiguration) *appsv1ac.StatefulSetApplyConfiguration {
	labels := selectorLabels(dc)
	container := torrentContainer(dc, image, dataDir, rt)
	spec := appsv1ac.StatefulSetSpec().
		WithReplicas(torrentReplicas(dc)).
		WithSelector(metav1ac.LabelSelector().WithMatchLabels(labels)).
		WithTemplate(podTemplateAC(engineConfigHash(dc, nil), labels, podSpecAC(dc, container, dataClaimName, rt)))

	return appsv1ac.StatefulSet(name, dc.Namespace).
		WithLabels(labels).
		WithOwnerReferences(owner).
		WithSpec(spec)
}

// buildDeployment renders the usenet engine's Deployment: always one replica
// (DownloadClientSpec's CEL rule enforces spec.replicas==1 for usenet), mounting
// /data and a scratch volume, running [usenetContainer].
//
// Its strategy is Recreate. The default RollingUpdate surges a second pod
// before stopping the first, and two usenet engines under one --engine
// identity would both reconcile the same Downloads -- each re-adding every
// job to its own scratch area, fetching the same articles twice against the
// providers' connection limits and quotas -- while a scratch PVC, which is
// ReadWriteOnce, leaves the surge pod unschedulable on another node and the
// rollout stuck. A roll is routine now that a config change triggers one
// ([EngineConfigHashAnnotation]), so the old engine stops first.
//
// secrets is the digest of each provider Secret ([Reconciler.secretDigests]),
// folded into the config hash so a rotated credential rolls the engine too.
func buildDeployment(dc *downloadv1alpha1.DownloadClient, name, image, dataDir, scratchDir, dataClaimName string, rt EngineRuntime, secrets map[string]string, owner *metav1ac.OwnerReferenceApplyConfiguration) *appsv1ac.DeploymentApplyConfiguration {
	labels := selectorLabels(dc)
	container := usenetContainer(dc, image, dataDir, scratchDir, rt)
	spec := appsv1ac.DeploymentSpec().
		WithReplicas(1).
		WithStrategy(appsv1ac.DeploymentStrategy().WithType(appsv1.RecreateDeploymentStrategyType)).
		WithSelector(metav1ac.LabelSelector().WithMatchLabels(labels)).
		WithTemplate(podTemplateAC(engineConfigHash(dc, secrets), labels, podSpecAC(dc, container, dataClaimName, rt, scratchVolume(dc))))

	return appsv1ac.Deployment(name, dc.Namespace).
		WithLabels(labels).
		WithOwnerReferences(owner).
		WithSpec(spec)
}
