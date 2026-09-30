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
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// countingReader counts the TranscodeJob Lists read through it: admission's
// uncached read of every job.
type countingReader struct {
	client.Reader
	jobLists atomic.Int32
}

func (c *countingReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if _, ok := list.(*transcodev1alpha1.TranscodeJobList); ok {
		c.jobLists.Add(1)
	}
	return c.Reader.List(ctx, list, opts...)
}

// A job's own reconcile wakes admission rather than running it: admission
// lists every TranscodeJob uncached, and running it inline in every job's
// reconcile made a re-roll quadratic -- 8,146 jobs, an 11 MB list per
// reconcile, about 2 jobs a second through the single worker, and slots
// held by deleting jobs until their turn came. The wake enqueues the one
// admission request, which the workqueue dedups, so a burst of reconciles
// runs one pass.
func TestAJobsReconcileWakesAdmissionInsteadOfRunningIt(t *testing.T) {
	later := metav1.NewTime(time.Now().Add(time.Hour))
	tj := &transcodev1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "job"},
		Spec:       transcodev1alpha1.TranscodeJobSpec{MediaFileRef: "mf", ProfileRef: "p", SourcePath: "/data/x.mkv", SourceProbeHash: "h"},
		Status: transcodev1alpha1.TranscodeJobStatus{
			Phase:         transcodev1alpha1.TranscodeJobPhasePlanned,
			Plan:          &transcodev1alpha1.Plan{Mode: transcodev1alpha1.PlanModeTranscode, Encoder: "hevc_nvenc"},
			NextAttemptAt: &later, // waits for its backoff: this pass does nothing else
		},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithObjects(tj).WithStatusSubresource(tj).Build()
	reader := &countingReader{Reader: c}
	r := &Reconciler{Client: c, Reader: reader, wake: make(chan event.GenericEvent, 1)}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "job"}})
	require.NoError(t, err)
	assert.Zero(t, reader.jobLists.Load(), "a job's reconcile listed every TranscodeJob")
	assert.Len(t, r.wake, 1, "a job's reconcile did not wake admission")
}
