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

package remediation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/graftplan"
	"github.com/mediactl/clustarr/app/transcode/grafttask"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestGraftJobNameIsTheDispatchIdentity (§5.13, §5.17): a re-created Job of
// the same dispatch is AlreadyExists, counted done; a deleted one is
// NotFound, counted done; any other effect is not a graft Job's.
func TestGraftJobNameIsTheDispatchIdentity(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	name := graftplan.JobName("pb", 100)
	assert.Equal(t, name, graftplan.JobName("pb", 100), "one dispatch, one name")
	assert.NotEqual(t, name, graftplan.JobName("pb", 101), "another dispatch, another Job")
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: name}}
	ok, err := ExecGraftJobEffect(t.Context(), c, CreateGraftJob{Job: job.DeepCopy()})
	require.True(t, ok)
	require.NoError(t, err)
	ok, err = ExecGraftJobEffect(t.Context(), c, CreateGraftJob{Job: job.DeepCopy()})
	require.True(t, ok)
	require.NoError(t, err, "AlreadyExists is done")
	var jobs batchv1.JobList
	require.NoError(t, c.List(t.Context(), &jobs))
	assert.Len(t, jobs.Items, 1)
	for range 2 {
		ok, err = ExecGraftJobEffect(t.Context(), c, DeleteGraftJob{Namespace: "media", Name: job.Name})
		require.True(t, ok)
		require.NoError(t, err, "NotFound is done")
	}
	require.NoError(t, c.List(t.Context(), &jobs))
	assert.Empty(t, jobs.Items)
	ok, err = ExecGraftJobEffect(t.Context(), c, CreateGraftJob{})
	assert.True(t, ok)
	assert.NoError(t, err, "a create with no Job is nothing to do")
	ok, _ = ExecGraftJobEffect(t.Context(), c, RecordWrite{})
	assert.False(t, ok, "not a graft Job effect")
}

// TestAGraftJobWakesTheFileThatOwnsIt (S15): a graft Job maps to its
// controlling MediaFile's file key; a Job without the label or that owner
// maps to nothing; only a change of finished-ness or a delete passes.
func TestAGraftJobWakesTheFileThatOwnsIt(t *testing.T) {
	mf := &catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "pb", UID: "mf-uid"}}
	j := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "pb-graft-1", Labels: map[string]string{grafttask.LabelGraftOf: "mf-uid"}}}
	j.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(mf, catalogv1alpha1.GroupVersion.WithKind("MediaFile"))}
	assert.Equal(t, []Key{{Kind: KindMediaFile, Namespace: "media", Name: "pb"}}, GraftJobKeys(t.Context(), j))
	unlabelled := j.DeepCopy()
	unlabelled.Labels = nil
	assert.Empty(t, GraftJobKeys(t.Context(), unlabelled))
	orphan := j.DeepCopy()
	orphan.OwnerReferences = nil
	assert.Empty(t, GraftJobKeys(t.Context(), orphan))
	other := j.DeepCopy()
	other.OwnerReferences = []metav1.OwnerReference{*metav1.NewControllerRef(&catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: "pb", UID: "m"}},
		catalogv1alpha1.GroupVersion.WithKind("Movie"))}
	assert.Empty(t, GraftJobKeys(t.Context(), other), "a Job another kind controls")

	p := graftJobPredicate()
	done := j.DeepCopy()
	done.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
	assert.True(t, p.Update(event.TypedUpdateEvent[*batchv1.Job]{ObjectOld: j, ObjectNew: done}))
	complete := j.DeepCopy()
	complete.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	assert.True(t, p.Update(event.TypedUpdateEvent[*batchv1.Job]{ObjectOld: j, ObjectNew: complete}))
	assert.False(t, p.Update(event.TypedUpdateEvent[*batchv1.Job]{ObjectOld: done, ObjectNew: done.DeepCopy()}))
	progress := j.DeepCopy()
	progress.Status.Active = 1
	assert.False(t, p.Update(event.TypedUpdateEvent[*batchv1.Job]{ObjectOld: j, ObjectNew: progress}), "progress is nothing the planner reads")
	assert.True(t, p.Delete(event.TypedDeleteEvent[*batchv1.Job]{Object: j}))
	assert.False(t, p.Create(event.TypedCreateEvent[*batchv1.Job]{Object: j}), "the initial list already enqueues every file")
	assert.False(t, p.Generic(event.TypedGenericEvent[*batchv1.Job]{Object: j}))
}
