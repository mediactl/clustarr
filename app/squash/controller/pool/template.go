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

package pool

import (
	"cmp"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/worker"
)

// Labels, annotations and env names squasharr-pool stamps on the Jobs and
// pods it creates.
const (
	// LabelManagedBy/ManagedByValue select every pool Job squasharr owns.
	LabelManagedBy = "app.kubernetes.io/managed-by"
	ManagedByValue = "squasharr"

	// LabelHardware is the hardware class a pool's pods run under.
	LabelHardware = "transcode.clustarr.io/hardware"

	// ContainerName is the transcode container's name in a pool pod.
	ContainerName = "transcode"

	// EnvProfileUID and EnvClass identify a pool's (profile, class) key to
	// its own pods, for the worker's logs and metrics.
	EnvProfileUID = "CLUSTARR_POOL_PROFILE_UID"
	EnvClass      = "CLUSTARR_POOL_CLASS"

	// DefaultDataClaimName is the RWX claim every clustarr pod mounts at
	// /data, the same "clustarr-data" grabarr's DefaultDataClaimName names.
	DefaultDataClaimName = "clustarr-data"

	// DefaultDataDir is where a pool pod mounts it and what --data-dir says.
	DefaultDataDir = "/data"

	// UmaskEnv is §11's UMASK, passed through to the worker so it creates
	// files with the same mode the Deployments do.
	UmaskEnv = "UMASK"

	// DefaultNodeLabelNVIDIA and DefaultNodeLabelIntel are the GPU node
	// labels a pool's nodes are held to when the operator sets neither
	// --gpu-node-label-nvidia nor --gpu-node-label-intel (spec §18.5).
	DefaultNodeLabelNVIDIA = "nvidia.com/gpu.present"
	DefaultNodeLabelIntel  = "intel.feature.node.kubernetes.io/gpu"
)

// Pod-shape constants: volume names and mount paths every pool pod carries.
const (
	DataVolumeName    = "data"
	ScratchVolumeName = "scratch"
	ScratchMountPath  = "/scratch"
	TmpVolumeName     = "tmp"
	TmpMountPath      = "/tmp"

	// podUID and podGID are the identity every pool pod runs as: the
	// clustarr user images/Dockerfile.media and Dockerfile.media-cuda
	// create, and the runAsUser/runAsGroup/fsGroup every Deployment under
	// config/manager sets. TestJobPodSecurityMatchesTheDeployments holds
	// the pool pod to config/manager/squasharr.yaml.
	podUID = int64(1000)
	podGID = int64(1000)
)

// GPUResource maps a hardware class to the extended resource name its GPU
// count is requested under.
var GPUResource = map[transcodev1alpha1.Hardware]corev1.ResourceName{
	transcodev1alpha1.HardwareNVIDIA: "nvidia.com/gpu",
	transcodev1alpha1.HardwareIntel:  "gpu.intel.com/i915",
}

// Config is everything about a pool that does not come from the
// TranscodeProfile: deployment-level settings threaded in from flags.
type Config struct {
	// Namespace is where every pool Job is created: squasharr's own.
	Namespace string

	// Image runs cpu and intel pools (--worker-image).
	Image string

	// ImageCUDA runs nvidia pools (--worker-image-cuda). Empty falls back
	// to Image.
	ImageCUDA string

	// DataClaimName is the RWX PersistentVolumeClaim mounted at DataDir.
	// Empty means DefaultDataClaimName.
	DataClaimName string

	// DataDir is the mount path and the --data-dir value. Empty means
	// DefaultDataDir.
	DataDir string

	// Umask, when set, is exported to the worker as UMASK (§11: every
	// media-touching pod creates files with UMASK 002).
	Umask string

	// NATSURL is exported to the worker as NATS_URL, the bus a pool's pods
	// pull tasks from and publish results and telemetry to.
	NATSURL string

	// IntelRenderGroups are the supplementalGroups an Intel (QSV/VAAPI) pool
	// pod gets, so its non-root user can open the /dev/dri/renderD* node the
	// Intel GPU device plugin mounts (§6.4: "supplementalGroups render").
	IntelRenderGroups []int64

	// ExtraArgs are appended after the fixed worker arguments (for example
	// --nats-single-node on kind).
	ExtraArgs []string

	// NodeLabelNVIDIA and NodeLabelIntel override the GPU node label a
	// class's pools are held to; empty means DefaultNodeLabelNVIDIA /
	// DefaultNodeLabelIntel.
	NodeLabelNVIDIA string
	NodeLabelIntel  string
}

// NodeLabel is the GPU node label key a class's pools are held to; "" for
// cpu.
func (c Config) NodeLabel(class transcodev1alpha1.Hardware) string {
	switch class {
	case transcodev1alpha1.HardwareNVIDIA:
		return cmp.Or(c.NodeLabelNVIDIA, DefaultNodeLabelNVIDIA)
	case transcodev1alpha1.HardwareIntel:
		return cmp.Or(c.NodeLabelIntel, DefaultNodeLabelIntel)
	}
	return ""
}

// Key identifies one pool: the profile it renders and the hardware class its
// pods run under.
type Key struct {
	Profile    string
	ProfileUID types.UID
	Class      transcodev1alpha1.Hardware
}

// The TranscodeProfile defaults Template floors a zero to. Each restates a
// +kubebuilder:default in transcodeprofile_types.go, and
// app/squash/controller/pool/template_test.go:TestFlooredDefaultsMatchTheGeneratedCRD
// holds them to the generated CRD.
//
// They exist because a kubebuilder default fills only an ABSENT field, and
// encoding/json always sends a struct: a profile created by a Go client
// carries resources {} and scratch "0", present and zero, so the apiserver
// defaults neither of them. Unfloored, a pool would run with no resource
// limits and an unbounded scratch volume. Neither zero has a coherent
// meaning, which is the D1 precedent's test for flooring
// (Indexer.spec.timeout).
var defaultScratch = resource.MustParse("20Gi")

func defaultResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{Limits: corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("8"),
		corev1.ResourceMemory: resource.MustParse("4Gi"),
	}}
}

// defaultThreads is x265's pools= for a pod whose container has neither a
// CPU limit nor a CPU request: the 8 cores of the CPU limit a profile with
// no resources at all runs with (defaultResources).
var defaultThreads = wholeCores(defaultResources().Limits[corev1.ResourceCPU])

// wholeCores rounds a CPU quantity up to whole cores, as the Downward API
// renders limits.cpu with divisor 1.
func wholeCores(q resource.Quantity) int32 {
	return int32((q.MilliValue() + 999) / 1000) //nolint:gosec // a pod's CPU count, far below int32's range
}

// ThreadsFromResources is x265's pools= size for a pod container with
// resources r, and whether it comes from a CPU limit.
//
// With a CPU limit it is the limit rounded up to whole cores: exactly what
// the Downward API renders into worker.CPULimitEnv, which Template then
// wires from limits.cpu (§6.4). With no limit the Downward API would render
// the NODE's allocatable CPU, which the controller planning the job cannot
// know, so status.plan's pools= would differ from the worker's argv. Such a
// pod is given a stated default instead, as a literal CLUSTARR_CPU_LIMIT
// value the planner uses too: the CPU request rounded up (the share the
// scheduler guarantees), else [defaultThreads].
func ThreadsFromResources(r corev1.ResourceRequirements) (threads int32, fromLimit bool) {
	if cpu, ok := r.Limits[corev1.ResourceCPU]; ok && cpu.Sign() > 0 {
		return wholeCores(cpu), true
	}
	if cpu, ok := r.Requests[corev1.ResourceCPU]; ok && cpu.Sign() > 0 {
		return wholeCores(cpu), false
	}
	return defaultThreads, false
}

// ResourcesFor floors an entirely empty spec.resources at the CRD default.
// Anything the operator did set is kept as it is: a profile with only a
// memory limit meant exactly that.
func ResourcesFor(p *transcodev1alpha1.TranscodeProfile) corev1.ResourceRequirements {
	r := p.Spec.Resources
	if len(r.Limits) == 0 && len(r.Requests) == 0 && len(r.Claims) == 0 {
		return defaultResources()
	}
	return *r.DeepCopy()
}

// scratchFor floors spec.scratch at the CRD default.
func scratchFor(p *transcodev1alpha1.TranscodeProfile) resource.Quantity {
	if p.Spec.Scratch.Sign() > 0 {
		return p.Spec.Scratch.DeepCopy()
	}
	return defaultScratch.DeepCopy()
}

// ScratchSource is the scratch emptyDir a pool pod mounts, sized from the
// profile with its CRD default floor applied.
func ScratchSource(p *transcodev1alpha1.TranscodeProfile) *corev1.EmptyDirVolumeSource {
	q := scratchFor(p)
	return &corev1.EmptyDirVolumeSource{SizeLimit: &q}
}

// AddGPU requests count of a GPU resource. Extended resources cannot be
// overcommitted, so the request, when set, must equal the limit; setting
// only the limit lets the apiserver default the request to it.
func AddGPU(rr *corev1.ResourceRequirements, name corev1.ResourceName, count int64) {
	if rr.Limits == nil {
		rr.Limits = corev1.ResourceList{}
	}
	q := *resource.NewQuantity(count, resource.DecimalSI)
	rr.Limits[name] = q
	if rr.Requests != nil {
		if _, ok := rr.Requests[name]; ok {
			rr.Requests[name] = q
		}
	}
}

// RequireNodeLabel is the pod-level fallback placement for a GPU class: on a
// cluster without WorkloadWithJob, this required node affinity is what holds
// the pool's pods to the label's "true" domain.
func RequireNodeLabel(key string) *corev1.Affinity {
	return &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key: key, Operator: corev1.NodeSelectorOpIn, Values: []string{"true"},
				}},
			}},
		},
	}}
}

// PodSecurityContext is the pod half of the security settings every
// Deployment under config/manager carries: non-root as the images' clustarr
// user, the RuntimeDefault seccomp profile, and fsGroup on the RWX /data
// volume with OnRootMismatch so a large library is not re-chowned on every
// pod start.
func PodSecurityContext() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		RunAsNonRoot:        ptr.To(true),
		RunAsUser:           ptr.To(podUID),
		RunAsGroup:          ptr.To(podGID),
		FSGroup:             ptr.To(podGID),
		FSGroupChangePolicy: ptr.To(corev1.FSGroupChangeOnRootMismatch),
		SeccompProfile:      &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

// ContainerSecurityContext is the container half: no privilege escalation,
// every capability dropped, and a read-only root filesystem. The worker
// writes only under /data (the .part output beside the source), /scratch and
// /tmp, and each of those is a volume.
func ContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		ReadOnlyRootFilesystem:   ptr.To(true),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}

func fieldRef(path string) *corev1.EnvVarSource {
	return &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: path}}
}

// cpuLimitEnv is worker.CPULimitEnv's entry (§6.4): x265 reads the host's CPU
// count, not the cgroup quota, so the worker sizes its thread pools from
// CLUSTARR_CPU_LIMIT -- the Downward API's limits.cpu when the container has
// a CPU limit, else the stated default Threads used, never the node's CPUs.
func cpuLimitEnv(threads int32, fromLimit bool) corev1.EnvVar {
	if fromLimit {
		return corev1.EnvVar{Name: worker.CPULimitEnv, ValueFrom: &corev1.EnvVarSource{ResourceFieldRef: &corev1.ResourceFieldSelector{
			ContainerName: ContainerName, Resource: "limits.cpu", Divisor: resource.MustParse("1"),
		}}}
	}
	return corev1.EnvVar{Name: worker.CPULimitEnv, Value: strconv.Itoa(int(threads))}
}

// Template is the pod template a (profile, class) pool asks for. The image,
// runtimeClassName, securityContext, volumes, env and args are immutable for
// the pool's life; resources, nodeSelector and tolerations can be changed
// while it is suspended (spec §3, §7).
func Template(tp *transcodev1alpha1.TranscodeProfile, class transcodev1alpha1.Hardware, cfg Config) corev1.PodTemplateSpec {
	dataDir := cmp.Or(cfg.DataDir, DefaultDataDir)
	image := cfg.Image
	if class == transcodev1alpha1.HardwareNVIDIA && cfg.ImageCUDA != "" {
		image = cfg.ImageCUDA
	}
	res := ResourcesFor(tp)
	threads, fromLimit := ThreadsFromResources(res)
	env := []corev1.EnvVar{
		{Name: "POD_NAME", ValueFrom: fieldRef("metadata.name")},
		{Name: "POD_NAMESPACE", ValueFrom: fieldRef("metadata.namespace")},
		{Name: "NODE_NAME", ValueFrom: fieldRef("spec.nodeName")},
		{Name: EnvProfileUID, Value: string(tp.UID)},
		{Name: EnvClass, Value: string(class)},
		{Name: "NATS_URL", Value: cfg.NATSURL},
		cpuLimitEnv(threads, fromLimit),
	}
	if cfg.Umask != "" {
		env = append(env, corev1.EnvVar{Name: UmaskEnv, Value: cfg.Umask})
	}
	pod := corev1.PodSpec{
		RestartPolicy:                corev1.RestartPolicyNever,
		AutomountServiceAccountToken: ptr.To(false),
		SecurityContext:              PodSecurityContext(),
		Containers: []corev1.Container{{
			Name:            ContainerName,
			Image:           image,
			Args:            append([]string{"--data-dir", dataDir}, cfg.ExtraArgs...),
			Env:             env,
			Resources:       res,
			SecurityContext: ContainerSecurityContext(),
			VolumeMounts: []corev1.VolumeMount{
				{Name: DataVolumeName, MountPath: dataDir},
				{Name: ScratchVolumeName, MountPath: ScratchMountPath},
				{Name: TmpVolumeName, MountPath: TmpMountPath},
			},
		}},
		Volumes: []corev1.Volume{
			{Name: DataVolumeName, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: cmp.Or(cfg.DataClaimName, DefaultDataClaimName)}}},
			{Name: ScratchVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: ScratchSource(tp)}},
			{Name: TmpVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		},
	}
	applyHardware(&pod, tp, class, cfg)
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
			"app.kubernetes.io/name": "clustarr", "app.kubernetes.io/component": "squasharr-worker",
			LabelManagedBy: ManagedByValue, LabelHardware: string(class),
		}},
		Spec: pod,
	}
}

// applyHardware sets the GPU resource request, runtimeClassName, node
// affinity and (Intel) supplementalGroups a class needs, and the profile's
// GPU nodeSelector/tolerations -- never for cpu, so a Dolby Vision source
// forced onto the CPU tier under a GPU profile is not pinned to (and does
// not tolerate the taints of) the GPU nodes.
func applyHardware(pod *corev1.PodSpec, tp *transcodev1alpha1.TranscodeProfile, class transcodev1alpha1.Hardware, cfg Config) {
	gpuCount := int64(1)
	if g := tp.Spec.GPU; g != nil && g.Count > 0 {
		gpuCount = int64(g.Count)
	}
	res := &pod.Containers[0].Resources
	switch class {
	case transcodev1alpha1.HardwareNVIDIA:
		AddGPU(res, GPUResource[transcodev1alpha1.HardwareNVIDIA], gpuCount)
		pod.Containers[0].Env = append(pod.Containers[0].Env,
			corev1.EnvVar{Name: "NVIDIA_DRIVER_CAPABILITIES", Value: "video,compute,utility"})
		rc := "nvidia"
		if g := tp.Spec.GPU; g != nil && g.RuntimeClassName != "" {
			rc = g.RuntimeClassName
		}
		pod.RuntimeClassName = ptr.To(rc)
		pod.Affinity = RequireNodeLabel(cfg.NodeLabel(class))
	case transcodev1alpha1.HardwareIntel:
		AddGPU(res, GPUResource[transcodev1alpha1.HardwareIntel], gpuCount)
		pod.Affinity = RequireNodeLabel(cfg.NodeLabel(class))
		if len(cfg.IntelRenderGroups) > 0 {
			pod.SecurityContext.SupplementalGroups = append([]int64(nil), cfg.IntelRenderGroups...)
		}
	}
	if g := tp.Spec.GPU; g != nil && class != transcodev1alpha1.HardwareCPU {
		if len(g.NodeSelector) > 0 {
			pod.NodeSelector = g.NodeSelector
		}
		pod.Tolerations = g.Tolerations
	}
}

// Threads is the x265 pool size a profile's pods get: plan.go plans with it.
func Threads(p *transcodev1alpha1.TranscodeProfile) int32 {
	n, _ := ThreadsFromResources(ResourcesFor(p))
	return n
}
