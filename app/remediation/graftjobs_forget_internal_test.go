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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/grafttask"
)

// A graft Job names its MediaFile as its controlling owner, and the
// apiserver's garbage collector deleted it with the file while a MediaFile
// was a custom resource. A MediaFile is a row now (ADR-0024) and that
// collector cannot follow the reference, so the pass a file's delete wakes
// deletes its Jobs: the deleted file's and no other's. Without the call in
// Reconcile's not-found branch the Job of a deleted file ran on to its
// deadline.
func TestADeletedFilesGraftJobsAreDeleted(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, batchv1.AddToScheme(scheme))
	job := func(name, file string, graft bool) *batchv1.Job {
		j := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"}}
		if graft {
			j.Labels = map[string]string{grafttask.LabelGraftOf: "uid-of-" + file}
		}
		isController := true
		j.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: catalogv1alpha1.GroupVersion.String(), Kind: "MediaFile", Name: file, UID: types.UID("uid-of-" + file), Controller: &isController,
		}}
		return j
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		job("graft-heat-1", "heat", true),
		job("graft-heat-2", "heat", true),
		job("graft-ronin-1", "ronin", true),
		job("pool-heat", "heat", false), // not a graft Job: no label
	).Build()
	r := &Reconciler{c: c, env: &Env{Reader: c}}

	r.forgetGraftJobs(t.Context(), types.NamespacedName{Namespace: "media", Name: "heat"})

	var left batchv1.JobList
	require.NoError(t, c.List(t.Context(), &left, client.InNamespace("media")))
	var names []string
	for _, j := range left.Items {
		names = append(names, j.Name)
	}
	assert.ElementsMatch(t, []string{"graft-ronin-1", "pool-heat"}, names)
}
