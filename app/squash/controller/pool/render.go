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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	batchv1ac "k8s.io/client-go/applyconfigurations/batch/v1"
	"k8s.io/utils/ptr"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/worker"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Labels, annotations and defaults a rendered pool Job carries.
const (
	// LabelProfile names the TranscodeProfile a pool renders, for
	// `kubectl get jobs -l ...`. Its value is [ProfileLabelValue]: the name
	// when it fits a label value, else a truncated, hashed form, so it
	// cannot be read back as the name. What identifies a pool is its name
	// ([Name], from the profile's UID) and its controller owner reference.
	LabelProfile = "transcode.clustarr.io/profile"

	// LabelTemplateHash is the immutable part's hash (see [Hash]): a
	// changed value is immutable drift, never reconciled in place.
	LabelTemplateHash = "squasharr.clustarr.io/template-hash"

	// AnnotationAppliedTemplate holds the JSON of the [Spec] this manager
	// last applied. Judging drift against it, not the stored Job's
	// spec.template, ignores apiserver defaulting (spec §3; Review Focus 2).
	AnnotationAppliedTemplate = "squasharr.clustarr.io/applied-template"

	// BackoffLimit bounds worker-level pod failures only (NATS unreachable,
	// bad environment): task failures never fail a pod, and the worker never
	// exits 0, so the pool Job would otherwise fail on legitimate drains.
	BackoffLimit = int32(6)
)

// ProfileLabelValue is a profile name as a label value. A TranscodeProfile
// name is a DNS subdomain, up to 253 characters of which a label value may
// hold 63 (R19): one that fits is itself; a longer one is its truncated
// prefix and a hash of the whole name.
func ProfileLabelValue(profile string) string {
	if len(profile) <= k8s.MaxLabelValueLength {
		return profile
	}
	return k8s.LabelSafeName(profile, profile)
}

// Name is a pool's Job name: readable where it fits, and always ending in a
// hash of the profile's UID and the class, within 63 characters.
func Name(k Key) string {
	return k8s.LabelSafeName("squasharr-pool-"+k.Profile+"-"+string(k.Class), string(k.ProfileUID), string(k.Class))
}

// Mutable reports whether the apiserver will accept a template change: the
// Job is suspended and the Job controller has cleared startTime (spec §3).
func Mutable(j *batchv1.Job) bool {
	return ptr.Deref(j.Spec.Suspend, false) && j.Status.StartTime == nil && j.Status.Active == 0
}

// Spec is everything about a pool that the profile and the flags decide: the
// pod template and, for a GPU class, the topology key its Job's
// .spec.scheduling.schedulingConstraints carries (spec §18.5).
type Spec struct {
	Template   corev1.PodTemplateSpec `json:"template"`
	Constraint string                 `json:"constraint,omitempty"`
}

// Want is the Spec a (profile, class) pool asks for.
func Want(tp *transcodev1alpha1.TranscodeProfile, class transcodev1alpha1.Hardware, cfg Config) Spec {
	return Spec{Template: Template(tp, class, cfg), Constraint: cfg.NodeLabel(class)}
}

// Hash identifies a Spec's immutable part. The constraint is in it: the
// apiserver never lets a Job's schedulingConstraints change.
func Hash(s Spec) string {
	c := s.Template.DeepCopy()
	c.Spec.NodeSelector, c.Spec.Tolerations = nil, nil
	for i := range c.Spec.Containers {
		c.Spec.Containers[i].Resources = corev1.ResourceRequirements{}
	}
	b, _ := json.Marshal(struct {
		T *corev1.PodTemplateSpec
		C string
	}{c, s.Constraint})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// applied is the Spec squasharr last applied to j, from its annotation.
// Judging drift against it, not j.Spec.Template, ignores apiserver defaulting.
func applied(j *batchv1.Job) (Spec, bool) {
	var s Spec
	raw, ok := j.Annotations[AnnotationAppliedTemplate]
	if !ok || json.Unmarshal([]byte(raw), &s) != nil {
		return s, false
	}
	return s, true
}

// Drift classifies how far a stored pool Job has drifted from what the
// profile and flags now ask for.
type Drift int

const (
	// DriftNone: the stored Job already matches what is wanted.
	DriftNone Drift = iota
	// DriftReshape: only the mutable part (resources, nodeSelector,
	// tolerations) differs. Applying it needs the Job suspended and idle.
	DriftReshape
	// DriftRecreate: the immutable part differs, or the applied template is
	// unknown. The Job must be drained, deleted and recreated.
	DriftRecreate
)

// Classify compares what the profile and flags now ask for with what was applied.
func Classify(stored *batchv1.Job, want Spec) Drift {
	prev, ok := applied(stored)
	if !ok || Hash(prev) != Hash(want) {
		return DriftRecreate
	}
	if !equality.Semantic.DeepEqual(mutablePart(prev.Template), mutablePart(want.Template)) {
		return DriftReshape
	}
	return DriftNone
}

type mutable struct {
	NodeSelector map[string]string
	Tolerations  []corev1.Toleration
	Resources    []corev1.ResourceRequirements
}

func mutablePart(t corev1.PodTemplateSpec) mutable {
	m := mutable{NodeSelector: t.Spec.NodeSelector, Tolerations: t.Spec.Tolerations}
	for _, c := range t.Spec.Containers {
		m.Resources = append(m.Resources, c.Resources)
	}
	return m
}

// Render is the one complete declaration squasharr-pool makes for a pool.
// An existing Job is always re-sent the constraint it was created with (it
// is immutable even while suspended). While the Job is not Mutable, its
// applied template is re-sent unchanged too: a template field this manager
// stops sending would be released, and a released template field on a
// running Job is a rejected write.
func Render(k Key, tp *transcodev1alpha1.TranscodeProfile, want Spec, d Desired,
	stored *batchv1.Job, cfg Config,
) (*batchv1ac.JobApplyConfiguration, error) {
	d.Parallelism = max(d.Parallelism, 1)
	spec := want
	if stored != nil {
		prev, ok := applied(stored)
		if !ok {
			return nil, fmt.Errorf("pool %s: %w", stored.Name, ErrNoAppliedSpec)
		}
		spec.Constraint = prev.Constraint
		if !Mutable(stored) {
			spec.Template = prev.Template
		}
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	sched := &batchv1.JobSchedulingConfiguration{
		SchedulingPolicy: &schedulingv1alpha3.WorkloadPodGroupSchedulingPolicy{
			Gang: &schedulingv1alpha3.WorkloadPodGroupGangSchedulingPolicy{MinCount: ptr.To(d.Parallelism)},
		},
	}
	if spec.Constraint != "" {
		sched.SchedulingConstraints = &schedulingv1alpha3.WorkloadPodGroupSchedulingConstraints{
			Topology: []schedulingv1alpha3.TopologyConstraint{{Key: spec.Constraint}},
		}
	}
	job := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{
			Name: Name(k), Namespace: cfg.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name": "clustarr", "app.kubernetes.io/component": "squasharr-worker",
				LabelManagedBy: ManagedByValue, LabelHardware: string(k.Class), LabelProfile: ProfileLabelValue(k.Profile),
				LabelTemplateHash: Hash(spec),
			},
			Annotations: map[string]string{AnnotationAppliedTemplate: string(raw)},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: transcodev1alpha1.GroupVersion.String(), Kind: "TranscodeProfile",
				Name: tp.Name, UID: tp.UID, Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true),
			}},
		},
		Spec: batchv1.JobSpec{
			Parallelism:          ptr.To(d.Parallelism),
			Suspend:              ptr.To(d.Suspend),
			CompletionMode:       ptr.To(batchv1.NonIndexedCompletion),
			BackoffLimit:         ptr.To(BackoffLimit),
			PodReplacementPolicy: ptr.To(batchv1.Failed),
			PodFailurePolicy:     podFailurePolicy(),
			Scheduling:           sched,
			Template:             spec.Template,
		},
	}
	return toApply(job)
}

// toApply turns a typed Job into its apply configuration through JSON; the
// two share field names by construction.
func toApply(job *batchv1.Job) (*batchv1ac.JobApplyConfiguration, error) {
	b, err := json.Marshal(job)
	if err != nil {
		return nil, err
	}
	ac := batchv1ac.Job(job.Name, job.Namespace)
	if err := json.Unmarshal(b, ac); err != nil {
		return nil, err
	}
	ac.Status = nil
	return ac, nil
}

// podFailurePolicy is ruling R4 for a pool pod, against the process-level
// exit codes app/squash/worker declares: a pod evicted, preempted or drained
// (DisruptionTarget) is replaced without spending a retry; a worker that
// exits WorkerExitDrained (SIGTERM: scaling the pool down) is ignored too, so
// it never counts against backoffLimit; and WorkerExitMisconfigured fails the
// whole Job at once, because retrying would restart into the same bad
// environment. Every other non-zero exit, WorkerExitRetriable included, is
// retried up to BackoffLimit.
func podFailurePolicy() *batchv1.PodFailurePolicy {
	return &batchv1.PodFailurePolicy{Rules: []batchv1.PodFailurePolicyRule{
		{Action: batchv1.PodFailurePolicyActionIgnore, OnPodConditions: []batchv1.PodFailurePolicyOnPodConditionsPattern{
			{Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue},
		}},
		{Action: batchv1.PodFailurePolicyActionIgnore, OnExitCodes: &batchv1.PodFailurePolicyOnExitCodesRequirement{
			ContainerName: ptr.To(ContainerName), Operator: batchv1.PodFailurePolicyOnExitCodesOpIn,
			Values: []int32{worker.WorkerExitDrained},
		}},
		{Action: batchv1.PodFailurePolicyActionFailJob, OnExitCodes: &batchv1.PodFailurePolicyOnExitCodesRequirement{
			ContainerName: ptr.To(ContainerName), Operator: batchv1.PodFailurePolicyOnExitCodesOpIn,
			Values: []int32{worker.WorkerExitMisconfigured},
		}},
	}}
}
