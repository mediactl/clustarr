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
	"testing"

	"github.com/stretchr/testify/assert"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

func TestClassifyIgnoresApiserverDefaulting(t *testing.T) { // Review Focus 2
	k := Key{Profile: "p", ProfileUID: "u", Class: "cpu"}
	tp := profile()
	stored := rendered(t, k, tp, Desired{Parallelism: 1, Suspend: true}, nil)
	// What the apiserver does on create: requests copied from limits, fields defaulted.
	c := &stored.Spec.Template.Spec.Containers[0]
	c.Resources.Requests = c.Resources.Limits.DeepCopy()
	c.TerminationMessagePath, c.ImagePullPolicy = "/dev/termination-log", corev1.PullIfNotPresent
	assert.Equal(t, DriftNone, Classify(&stored, Want(tp, k.Class, cfg)))
}

func TestClassify(t *testing.T) {
	k := Key{Profile: "p", ProfileUID: "u", Class: "cpu"}
	stored := rendered(t, k, profile(), Desired{Parallelism: 1}, nil)
	reshaped := profile()
	reshaped.Spec.Resources.Limits[corev1.ResourceCPU] = resource.MustParse("8")
	assert.Equal(t, DriftReshape, Classify(&stored, Want(reshaped, k.Class, cfg)))
	other := cfg
	other.Image = "transcoder:new"
	assert.Equal(t, DriftRecreate, Classify(&stored, Want(profile(), k.Class, other)))
	delete(stored.Annotations, AnnotationAppliedTemplate)
	assert.Equal(t, DriftRecreate, Classify(&stored, Want(profile(), k.Class, cfg)), "an unknown applied template is recreated")
}

func job(suspend bool, par int32, started bool, active int32, failed bool) *batchv1.Job {
	j := &batchv1.Job{Spec: batchv1.JobSpec{Suspend: ptr.To(suspend), Parallelism: ptr.To(par)}}
	if started {
		j.Status.StartTime = &metav1.Time{}
	}
	j.Status.Active = active
	if failed {
		j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
	}
	return j
}

func TestNext(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stored     *batchv1.Job
		dispatched int32
		drift      Drift
		want       Desired
		act        Action
	}{
		{"no pool, no work", nil, 0, DriftNone, Desired{}, ActionNone},
		{"no pool, work", nil, 2, DriftNone, Desired{Parallelism: 2}, ActionApply},
		{"idle and suspended", job(true, 2, false, 0, false), 0, DriftNone, Desired{Parallelism: 2, Suspend: true}, ActionNone},
		{"work drained: suspend", job(false, 2, true, 2, false), 0, DriftNone, Desired{Parallelism: 2, Suspend: true}, ActionApply},
		{"work arrives: resume", job(true, 2, false, 0, false), 3, DriftNone, Desired{Parallelism: 3}, ActionApply},
		{"more work: scale up", job(false, 2, true, 2, false), 4, DriftNone, Desired{Parallelism: 4}, ActionApply},
		{"less work: never shrink below zero", job(false, 3, true, 3, false), 1, DriftNone, Desired{Parallelism: 3}, ActionNone},
		{"failed pool is recreated", job(false, 2, true, 0, true), 2, DriftNone, Desired{}, ActionDelete},
		{"drift, busy: hold", job(false, 2, true, 2, false), 2, DriftReshape, Desired{Parallelism: 2}, ActionNone},
		{"drift, drained: suspend", job(false, 2, true, 2, false), 0, DriftReshape, Desired{Parallelism: 2, Suspend: true}, ActionApply},
		{"drift, suspending: wait for startTime", job(true, 2, true, 1, false), 0, DriftReshape, Desired{Parallelism: 2, Suspend: true}, ActionNone},
		{"reshape when mutable", job(true, 2, false, 0, false), 0, DriftReshape, Desired{Parallelism: 1, Suspend: true}, ActionApply},
		{"recreate when mutable", job(true, 2, false, 0, false), 0, DriftRecreate, Desired{}, ActionDelete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, a := Next(tc.stored, tc.dispatched, tc.drift)
			assert.Equal(t, tc.act, a)
			if a != ActionDelete {
				assert.Equal(t, tc.want, d)
			}
		})
	}
}
