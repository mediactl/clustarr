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

package transcodeprofile

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// A profile keeps at most Window non-terminal TranscodeJobs, taking new
// files in name order, instead of one job per matching file: a re-roll on
// the owner's library created and deleted 10-20k objects. A Succeeded job
// is deleted once it is older than Retention AND its MediaFile was probed
// after it finished -- catalogarr incorporates a swap by reading the
// Succeeded job, so it must outlive that.
func TestAProfileKeepsAWindowOfJobsAndRetiresIncorporatedSuccesses(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	at := func(ago time.Duration) *metav1.Time { t := metav1.NewTime(now.Add(-ago)); return &t }

	tp := &transcodev1alpha1.TranscodeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "hevc"},
		Spec:       transcodev1alpha1.TranscodeProfileSpec{Default: true},
	}
	hash := profileHash(tp.Spec)
	tag := profileTag(tp.Name, hash)

	b := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithStatusSubresource(&transcodev1alpha1.TranscodeProfile{}, &transcodev1alpha1.TranscodeJob{}, &catalogv1alpha1.MediaFile{}).
		WithObjects(tp)
	file := func(name string, probedAgo time.Duration, done bool) {
		mf := &catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media", UID: types.UID("uid-" + name)},
			Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}, Path: "/data/movies/" + name + ".mkv"},
			Status: catalogv1alpha1.MediaFileStatus{
				ProbeHash: "p-" + name, ProbedAt: at(probedAgo),
				MediaInfo: &commonv1.MediaInfo{VideoCodec: "h264"},
			},
		}
		if done {
			mf.Status.MediaInfo.TranscodeProfile = tag
		}
		b = b.WithObjects(mf, &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"}})
	}
	job := func(file string, phase transcodev1alpha1.TranscodeJobPhase, finishedAgo time.Duration) {
		tj := &transcodev1alpha1.TranscodeJob{
			ObjectMeta: metav1.ObjectMeta{Name: transcodeJobName(file, hash), Namespace: "media"},
			Spec:       transcodev1alpha1.TranscodeJobSpec{MediaFileRef: file, ProfileRef: "hevc", SourcePath: "/data/movies/" + file + ".mkv", SourceProbeHash: "p-" + file},
			Status:     transcodev1alpha1.TranscodeJobStatus{Phase: phase},
		}
		if phase == transcodev1alpha1.TranscodeJobPhaseSucceeded {
			tj.Status.FinishedAt = at(finishedAgo)
		}
		b = b.WithObjects(tj)
	}

	for _, n := range []string{"a", "b", "c", "d"} {
		file(n, time.Hour, false)
	}
	job("a", transcodev1alpha1.TranscodeJobPhasePlanned, 0)
	file("old-done", time.Hour, true) // probed an hour ago, after the job finished two hours ago
	job("old-done", transcodev1alpha1.TranscodeJobPhaseSucceeded, 2*time.Hour)
	file("fresh-done", 10*time.Minute, true)
	job("fresh-done", transcodev1alpha1.TranscodeJobPhaseSucceeded, 30*time.Minute)
	file("unincorporated", 3*time.Hour, false) // probed before the job finished: catalogarr has not read it yet
	job("unincorporated", transcodev1alpha1.TranscodeJobPhaseSucceeded, 2*time.Hour)

	c := b.Build()
	r := NewReconciler(c, k8s.MustNewScheme(), nil)
	r.Window, r.Retention, r.Now = 2, time.Hour, func() time.Time { return now }

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Name: "hevc"}})
	require.NoError(t, err)

	var jobs transcodev1alpha1.TranscodeJobList
	require.NoError(t, c.List(context.Background(), &jobs))
	var got []string
	for _, tj := range jobs.Items {
		got = append(got, tj.Spec.MediaFileRef)
	}
	sort.Strings(got)
	assert.Equal(t, []string{"a", "b", "fresh-done", "unincorporated"}, got,
		"one new job fills the window of 2 (a is already Planned), by name; c and d wait; the incorporated old success is retired")
}
