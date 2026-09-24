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
	"testing"

	"github.com/stretchr/testify/assert"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/event"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/squasharr/controller/pool"
)

// TestTranscodeJobPredicateWakesOnADeadLetterAnnotation: the DLQ
// projector's annotation, and an operator removing it, change neither
// generation nor status -- without the predicate the fold in apply would
// never run. The worker's progress (a status write) and an unrelated
// annotation still do not wake the controller.
func TestTranscodeJobPredicateWakesOnADeadLetterAnnotation(t *testing.T) {
	p := transcodeJobPredicate()
	plain := &transcodev1alpha1.TranscodeJob{ObjectMeta: metav1.ObjectMeta{Name: "tj", Generation: 2}}
	annotated := plain.DeepCopy()
	annotated.Annotations = map[string]string{k8s.AnnotationDeadLettered: "subject@2026-09-23T10:00:00Z"}
	other := plain.DeepCopy()
	other.Annotations = map[string]string{"example.com/unrelated": "x"}
	progressed := plain.DeepCopy()
	progressed.Status.Progress = &transcodev1alpha1.Progress{Percent: 40}
	specChanged := plain.DeepCopy()
	specChanged.Generation = 3

	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: plain, ObjectNew: annotated}), "annotation added")
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: annotated, ObjectNew: plain}), "annotation removed")
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: plain, ObjectNew: specChanged}), "a spec change")
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: plain, ObjectNew: other}), "an unrelated annotation is not a wake")
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: plain, ObjectNew: progressed}), "the worker's progress is not a wake")
}

// TestPoolJobPredicate: the pool Job watch wakes admission on what a pass
// acts on -- suspend, pods, startTime (a template may change once it
// clears), failure and template hash -- and on a pool's creation and
// deletion, but not on churn it ignores (ready counts), nor on any Job that
// is not a pool.
func TestPoolJobPredicate(t *testing.T) {
	p := poolJobPredicate()
	running := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "squasharr-pool-hevc-cpu", Labels: map[string]string{
			pool.LabelManagedBy: pool.ManagedByValue, pool.LabelProfile: "hevc", pool.LabelTemplateHash: "h1",
		}},
		Spec:   batchv1.JobSpec{Suspend: ptr.To(false)},
		Status: batchv1.JobStatus{StartTime: &metav1.Time{}, Active: 2},
	}
	changed := func(mutate func(*batchv1.Job)) *batchv1.Job {
		j := running.DeepCopy()
		mutate(j)
		return j
	}
	for name, next := range map[string]*batchv1.Job{
		"suspended":        changed(func(j *batchv1.Job) { j.Spec.Suspend = ptr.To(true) }),
		"a pod went":       changed(func(j *batchv1.Job) { j.Status.Active = 1 }),
		"startTime clears": changed(func(j *batchv1.Job) { j.Status.StartTime = nil }),
		"failed": changed(func(j *batchv1.Job) {
			j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
		}),
		"template hash": changed(func(j *batchv1.Job) { j.Labels[pool.LabelTemplateHash] = "h2" }),
	} {
		assert.True(t, p.Update(event.UpdateEvent{ObjectOld: running, ObjectNew: next}), name)
	}
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: running, ObjectNew: changed(func(j *batchv1.Job) { j.Status.Ready = ptr.To(int32(2)) })}),
		"a ready count is not a wake")
	assert.True(t, p.Create(event.CreateEvent{Object: running}))
	assert.True(t, p.Delete(event.DeleteEvent{Object: running}))

	other := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "backup", Labels: map[string]string{"app": "backup"}}}
	assert.False(t, p.Create(event.CreateEvent{Object: other}), "another application's Job")
	assert.False(t, p.Delete(event.DeleteEvent{Object: other}))
	managedOnly := other.DeepCopy()
	managedOnly.Labels = map[string]string{pool.LabelManagedBy: pool.ManagedByValue}
	assert.False(t, p.Create(event.CreateEvent{Object: managedOnly}), "managed by squasharr but not a pool")
}
