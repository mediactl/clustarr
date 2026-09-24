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

package transcodejob

import (
	"strconv"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/squasharr/controller/pool"
	"github.com/mediactl/clustarr/squasharr/worker"
)

// Labels and annotations squasharr stamps on the Jobs it creates.
const (
	// LabelManagedBy/ManagedByValue select every transcode Job squasharr
	// owns; the admission pass lists by it.
	LabelManagedBy = "app.kubernetes.io/managed-by"
	ManagedByValue = "squasharr"

	// LabelHardware is the slot class the Job is budgeted against.
	LabelHardware = "transcode.clustarr.io/hardware"

	// AnnotationProfile is the TranscodeProfile name. An annotation, not a
	// label: a cluster-scoped name may be longer than a label value allows.
	AnnotationProfile = "transcode.clustarr.io/profile"

	// AnnotationTranscodeJob is the owning TranscodeJob's name, for
	// `kubectl get jobs -o yaml` readers; the owner reference is what the
	// code follows.
	AnnotationTranscodeJob = "transcode.clustarr.io/transcodejob"
)

// Pod-shape constants not moved to squasharr/controller/pool: containerName
// and backoffLimit are per-task-Job values, distinct from the pool's own
// ContainerName and BackoffLimit.
const (
	containerName = "transcode"
	backoffLimit  = int32(2)
)

// Moved to squasharr/controller/pool in Task 8 (the pool renderer): the
// volume names and mount paths, the GPU resource and node-label constants,
// DefaultDataClaimName, DefaultDataDir and UmaskEnv. These aliases keep
// buildJob and its remaining tests compiling until Task 10 deletes the
// per-task Job code (ruling R3); the exported ones (DefaultDataClaimName,
// UmaskEnv) are also squasharr/run.go's own reference, so they keep their
// casing here.
const (
	DefaultDataClaimName = pool.DefaultDataClaimName
	DefaultDataDir       = pool.DefaultDataDir
	UmaskEnv             = pool.UmaskEnv

	dataVolumeName    = pool.DataVolumeName
	scratchVolumeName = pool.ScratchVolumeName
	scratchMountPath  = pool.ScratchMountPath
	tmpVolumeName     = pool.TmpVolumeName
	tmpMountPath      = pool.TmpMountPath
)

// GPU resource names and node labels: superseded by pool.GPUResource (a map)
// and pool.DefaultNodeLabelNVIDIA/DefaultNodeLabelIntel (ruling R3). These
// local vars keep buildJob's switch statement unchanged until Task 10.
var (
	resourceNVIDIAGPU = pool.GPUResource[transcodev1alpha1.HardwareNVIDIA]
	resourceIntelGPU  = pool.GPUResource[transcodev1alpha1.HardwareIntel]
	nodeLabelNVIDIA   = pool.DefaultNodeLabelNVIDIA
	nodeLabelIntel    = pool.DefaultNodeLabelIntel
)

// Moved to squasharr/controller/pool in Task 8: resourcesFor,
// threadsFromResources, scratchSource, addGPU, requireNodeLabel,
// podSecurityContext and containerSecurityContext. buildJob calls these
// local aliases until Task 10 deletes it (ruling R3).
var (
	resourcesFor             = pool.ResourcesFor
	threadsFromResources     = pool.ThreadsFromResources
	scratchSource            = pool.ScratchSource
	addGPU                   = pool.AddGPU
	requireNodeLabel         = pool.RequireNodeLabel
	podSecurityContext       = pool.PodSecurityContext
	containerSecurityContext = pool.ContainerSecurityContext
)

// activeDeadlineFor floors spec.activeDeadline at the CRD default.
func activeDeadlineFor(p *transcodev1alpha1.TranscodeProfile) time.Duration {
	return worker.ActiveDeadline(p.Spec)
}

// JobConfig is everything about a transcode Job that does not come from the
// TranscodeJob or its profile: deployment-level settings task E-4 threads in
// from flags.
type JobConfig struct {
	// Image runs cpu and intel transcodes (--worker-image).
	Image string

	// ImageCUDA runs nvidia transcodes (--worker-image-cuda). Empty falls
	// back to Image.
	ImageCUDA string

	// DataClaimName is the RWX PersistentVolumeClaim mounted at DataDir.
	// Empty means DefaultDataClaimName.
	DataClaimName string

	// DataDir is the mount path and the --data-dir value. Empty means
	// DefaultDataDir.
	DataDir string

	// ServiceAccountName is the worker pod's service account. It needs the
	// worker's own RBAC (squasharr/worker/doc.go: transcodejobs get,
	// transcodejobs/status patch, transcodeprofiles get, mediafiles get,
	// rootfolders list) -- not this controller's. Empty uses the namespace
	// default, which has none of it.
	ServiceAccountName string

	// IntelRenderGroups are the supplementalGroups an Intel (QSV/VAAPI) Job
	// pod gets, so its non-root user can open the /dev/dri/renderD* node the
	// Intel GPU device plugin mounts (§6.4: "supplementalGroups render").
	// The node is owned by the HOST's render group, whose GID is allocated
	// per distribution and per install (commonly 104-110 or 992-993), so
	// there is no correct built-in value: the operator supplies it. Empty
	// sets none, which works only where the container runtime is configured
	// with device_ownership_from_security_context (containerd, CRI-O), which
	// hands the device to the pod's runAsUser/runAsGroup instead.
	IntelRenderGroups []int64

	// Umask, when set, is exported to the worker as UMASK (§11: every
	// media-touching pod creates files with UMASK 002). squasharr/run.go
	// passes the controller's own $UMASK through, so the Jobs follow
	// whatever the Deployment was given.
	Umask string

	// NATSURL, when set, is exported to the worker as NATS_URL, the bus the
	// worker writes its 1 Hz telemetry to (spec §5's clustarr-progress).
	// The worker does not need it (ruling R6: status.progress is the
	// record), so empty is legal and writes no telemetry; squasharr/run.go
	// passes the controller's own --nats-url.
	NATSURL string

	// ExtraArgs are appended after the fixed worker arguments (for example
	// --nats-single-node on kind). squasharr/run.go passes the controller's
	// --log-* and --tracing-* flags through here.
	ExtraArgs []string

	// TraceParent, when set, is exported to the worker as
	// worker.TraceParentEnv: the W3C traceparent of the reconcile that
	// created the Job, so the worker's spans continue its trace. ensureJob
	// sets it per Job; it is not a deployment setting.
	TraceParent string
}

// jobName is the batch Job's name: deterministic from the TranscodeJob's
// name and UID, and within the 63 characters the Job controller needs to put
// it in the batch.kubernetes.io/job-name label. The UID means a TranscodeJob
// deleted and recreated under the same name gets a fresh Job rather than
// adopting a finished one.
func jobName(tj *transcodev1alpha1.TranscodeJob) string {
	return k8s.LabelSafeName(tj.Name, tj.Namespace, tj.Name, string(tj.UID))
}

// buildJob renders the suspended batch/v1 Job for tj (§6.4, ADR-0005).
//
// hardware is the slot class decided from status.plan -- NOT the profile's
// spec.hardware, because the planner forces Dolby Vision onto the CPU tier
// and a remux-only plan needs no GPU at all.
func buildJob(tj *transcodev1alpha1.TranscodeJob, profile *transcodev1alpha1.TranscodeProfile,
	hardware transcodev1alpha1.Hardware, cfg JobConfig,
) *batchv1.Job {
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = DefaultDataDir
	}
	claim := cfg.DataClaimName
	if claim == "" {
		claim = DefaultDataClaimName
	}
	image := cfg.Image
	if hardware == transcodev1alpha1.HardwareNVIDIA && cfg.ImageCUDA != "" {
		image = cfg.ImageCUDA
	}

	name := jobName(tj)
	labels := map[string]string{
		"app.kubernetes.io/name":      "clustarr",
		"app.kubernetes.io/component": "squasharr-worker",
		LabelManagedBy:                ManagedByValue,
		LabelHardware:                 string(hardware),
	}
	annotations := map[string]string{
		AnnotationProfile:      tj.Spec.ProfileRef,
		AnnotationTranscodeJob: tj.Name,
	}

	args := []string{"squasharr", "--role", "worker", "--job", tj.Name, "--data-dir", dataDir}
	args = append(args, cfg.ExtraArgs...)

	resources := resourcesFor(profile)

	// §6.4: x265 reads the host's CPU count, not the cgroup quota, so the
	// worker sizes its thread pools from CLUSTARR_CPU_LIMIT: the Downward
	// API's limits.cpu when the container has a CPU limit, else the stated
	// default the planner used (threadsFromResources), never the node's CPUs.
	cpuEnv := corev1.EnvVar{Name: worker.CPULimitEnv, ValueFrom: &corev1.EnvVarSource{ResourceFieldRef: &corev1.ResourceFieldSelector{
		ContainerName: containerName, Resource: "limits.cpu", Divisor: resource.MustParse("1"),
	}}}
	if threads, fromLimit := threadsFromResources(resources); !fromLimit {
		cpuEnv = corev1.EnvVar{Name: worker.CPULimitEnv, Value: strconv.Itoa(int(threads))}
	}
	env := []corev1.EnvVar{
		{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
		{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
		cpuEnv,
	}
	if cfg.NATSURL != "" {
		env = append(env, corev1.EnvVar{Name: "NATS_URL", Value: cfg.NATSURL})
	}
	if cfg.Umask != "" {
		env = append(env, corev1.EnvVar{Name: UmaskEnv, Value: cfg.Umask})
	}
	if cfg.TraceParent != "" {
		env = append(env, corev1.EnvVar{Name: worker.TraceParentEnv, Value: cfg.TraceParent})
	}

	gpuCount := int64(1)
	if g := profile.Spec.GPU; g != nil && g.Count > 0 {
		gpuCount = int64(g.Count)
	}

	pod := corev1.PodSpec{
		RestartPolicy:      corev1.RestartPolicyNever,
		ServiceAccountName: cfg.ServiceAccountName,
		SecurityContext:    podSecurityContext(),
		Containers: []corev1.Container{{
			Name:            containerName,
			Image:           image,
			Args:            args,
			Env:             env,
			SecurityContext: containerSecurityContext(),
			VolumeMounts: []corev1.VolumeMount{
				{Name: dataVolumeName, MountPath: dataDir},
				{Name: scratchVolumeName, MountPath: scratchMountPath},
				{Name: tmpVolumeName, MountPath: tmpMountPath},
			},
		}},
		Volumes: []corev1.Volume{
			{Name: dataVolumeName, VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim},
			}},
			{Name: scratchVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: scratchSource(profile)}},
			{Name: tmpVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		},
	}

	switch hardware {
	case transcodev1alpha1.HardwareNVIDIA:
		addGPU(&resources, resourceNVIDIAGPU, gpuCount)
		pod.Containers[0].Env = append(pod.Containers[0].Env,
			corev1.EnvVar{Name: "NVIDIA_DRIVER_CAPABILITIES", Value: "video,compute,utility"})
		rc := "nvidia"
		if g := profile.Spec.GPU; g != nil && g.RuntimeClassName != "" {
			rc = g.RuntimeClassName
		}
		pod.RuntimeClassName = ptr.To(rc)
		pod.Affinity = requireNodeLabel(nodeLabelNVIDIA)
	case transcodev1alpha1.HardwareIntel:
		addGPU(&resources, resourceIntelGPU, gpuCount)
		pod.Affinity = requireNodeLabel(nodeLabelIntel)
		if len(cfg.IntelRenderGroups) > 0 {
			pod.SecurityContext.SupplementalGroups = append([]int64(nil), cfg.IntelRenderGroups...)
		}
	}
	pod.Containers[0].Resources = resources

	// The profile's GPU placement applies only to a GPU transcode: a Dolby
	// Vision source forced onto the CPU tier under a GPU profile must not be
	// pinned to (and tolerate the taints of) the GPU nodes.
	if g := profile.Spec.GPU; g != nil && hardware != transcodev1alpha1.HardwareCPU {
		if len(g.NodeSelector) > 0 {
			pod.NodeSelector = g.NodeSelector
		}
		pod.Tolerations = g.Tolerations
	}

	spec := batchv1.JobSpec{
		Suspend:              ptr.To(true),
		BackoffLimit:         ptr.To(backoffLimit),
		PodReplacementPolicy: ptr.To(batchv1.Failed),
		PodFailurePolicy:     podFailurePolicy(),
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotations},
			Spec:       pod,
		},
	}
	spec.ActiveDeadlineSeconds = ptr.To(int64(activeDeadlineFor(profile).Seconds()))
	if ttl := profile.Spec.TTLSecondsAfterFinished; ttl > 0 {
		spec.TTLSecondsAfterFinished = ptr.To(ttl)
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   tj.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: spec,
	}
}

// podFailurePolicy is ruling R4, against the exit codes squasharr/worker
// declares (worker.ExitInvalidSource = 3, worker.ExitVerifyFailed = 4). A
// pod evicted, preempted or drained (DisruptionTarget) is replaced without
// spending a retry, and a worker that exits 3 (the source is not the file
// that was planned) or 4 (the output failed verification) fails the whole
// Job at once -- retrying either would transcode the same bad input
// backoffLimit more times for the same answer. Every other non-zero exit,
// 2 included, is retried up to backoffLimit.
func podFailurePolicy() *batchv1.PodFailurePolicy {
	return &batchv1.PodFailurePolicy{Rules: []batchv1.PodFailurePolicyRule{
		{
			Action: batchv1.PodFailurePolicyActionIgnore,
			OnPodConditions: []batchv1.PodFailurePolicyOnPodConditionsPattern{{
				Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue,
			}},
		},
		{
			Action: batchv1.PodFailurePolicyActionFailJob,
			OnExitCodes: &batchv1.PodFailurePolicyOnExitCodesRequirement{
				ContainerName: ptr.To(containerName),
				Operator:      batchv1.PodFailurePolicyOnExitCodesOpIn,
				Values:        []int32{worker.ExitInvalidSource, worker.ExitVerifyFailed},
			},
		},
	}}
}

// jobFinished reports whether the Job reached a terminal condition, and
// which. A Job is finished only once Complete or Failed is True; the
// transient SuccessCriteriaMet/FailureTarget conditions precede those while
// the Job controller is still cleaning up pods.
func jobFinished(j *batchv1.Job) (done, succeeded bool, cond *batchv1.JobCondition) {
	for i := range j.Status.Conditions {
		c := &j.Status.Conditions[i]
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return true, true, c
		case batchv1.JobFailed:
			return true, false, c
		}
	}
	return false, false, nil
}

// jobSuspended reports spec.suspend.
func jobSuspended(j *batchv1.Job) bool {
	return j.Spec.Suspend != nil && *j.Spec.Suspend
}
