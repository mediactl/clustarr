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
	"fmt"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Classes are the hardware classes a pool can be for: every class a task is
// dispatched to (auto and gpu are resolved before dispatch).
var Classes = []transcodev1alpha1.Hardware{
	transcodev1alpha1.HardwareCPU, transcodev1alpha1.HardwareNVIDIA, transcodev1alpha1.HardwareIntel,
}

// PoolJobCache is the one way the manager may cache batch/v1 Jobs (R21):
// the pool Jobs alone -- those in namespace, labelled managed-by=transcode
// -- rather than every Job in the cluster. A Job read through the cached
// client would therefore silently miss every other Job: a restricted cache
// answers NotFound for an object it does not hold, which is
// indistinguishable from a Job that does not exist. Read any other Job
// through the APIReader, or widen this first.
func PoolJobCache(namespace string) cache.ByObject {
	b := cache.ByObject{Label: labels.SelectorFromSet(labels.Set{LabelManagedBy: ManagedByValue})}
	if namespace != "" {
		b.Namespaces = map[string]cache.Config{namespace: {}}
	}
	return b
}

// IsPoolJob passes the Jobs the transcode pools are.
func IsPoolJob(o client.Object) bool {
	l := o.GetLabels()
	_, ok := l[LabelProfile]
	return ok && l[LabelManagedBy] == ManagedByValue
}

// JobSignal is what admission and the pool reconciler act on in a pool Job:
// its suspend, whether its pods are running and started (a template may
// change only once both are clear), whether it failed, and its template
// hash.
func JobSignal(o client.Object) string {
	j, ok := o.(*batchv1.Job)
	if !ok {
		return ""
	}
	return fmt.Sprintf("suspend=%t active=%d started=%t failed=%t hash=%s",
		ptr.Deref(j.Spec.Suspend, false), j.Status.Active, j.Status.StartTime != nil, Failed(j),
		j.Labels[LabelTemplateHash])
}

// JobPredicate passes a pool Job's creation, deletion and every change to
// its JobSignal, and nothing about any other Job.
func JobPredicate() predicate.Predicate {
	return k8s.And(predicate.NewPredicateFuncs(IsPoolJob), k8s.StatusFieldChanged(JobSignal))
}

// OwnerProfileUID is the UID of the TranscodeProfile that is j's controller
// owner, or "" when none is.
func OwnerProfileUID(j *batchv1.Job) types.UID {
	if ref := metav1.GetControllerOf(j); ref != nil && ref.Kind == "TranscodeProfile" &&
		strings.HasPrefix(ref.APIVersion, transcodev1alpha1.GroupVersion.Group+"/") {
		return ref.UID
	}
	return ""
}

// JobFinished reports whether j has a Complete or Failed condition, when
// it was set, and the Failed condition's message: a graft Job's end, which
// the remediation loop's graft planner reads (loop spec §4.12).
func JobFinished(j *batchv1.Job) (bool, time.Time, string) {
	for _, c := range j.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			msg := ""
			if c.Type == batchv1.JobFailed {
				msg = c.Message
			}
			return true, c.LastTransitionTime.Time, msg
		}
	}
	return false, time.Time{}, ""
}
