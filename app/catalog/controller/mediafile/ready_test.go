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

package mediafile

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// A file that went missing for a moment -- a transcode's swap caught
// mid-way, a share that blinked -- and came back unchanged has a current
// probe, so no probe runs; Ready, set only by a probe, read FileMissing
// for good (three files on the owner's library since 2026-09-30, the
// rescan rightly keeping them). The reconcile that finds it again says so.
func TestAFileBackFromMissingIsReadyAgainWithoutAProbe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Movie (2016).mkv")
	require.NoError(t, os.WriteFile(path, []byte("bytes"), 0o644))
	info, err := os.Stat(path)
	require.NoError(t, err)
	missingAt := metav1.NewTime(time.Date(2026, 9, 30, 16, 27, 41, 0, time.UTC))

	mf := &catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: "movie", Namespace: "media", Generation: 1}}
	mf.Spec.Path = path
	mf.Spec.MediaRef = commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "movie"}
	mf.Status.ProbeHash = mediainfo.ProbeHash(path, info.Size(), info.ModTime())
	mf.Status.ProbeVersion = mediainfo.ProbeVersion
	mf.Status.ProbedAt = &missingAt
	mf.Status.MediaInfo = &commonv1.MediaInfo{RuntimeMillis: 1000, VideoCodec: "hevc"}
	mf.Status.Conditions = []metav1.Condition{
		{Type: catalogv1alpha1.MediaFileConditionProbed, Status: metav1.ConditionTrue, Reason: "Probed", LastTransitionTime: missingAt},
		{Type: catalogv1alpha1.MediaFileConditionReady, Status: metav1.ConditionFalse, Reason: "FileMissing", Message: "stat: gone", LastTransitionTime: missingAt},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(mf).WithStatusSubresource(mf).Build()
	r := &Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Clock: time.Now,
		Probe: func(context.Context, string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
			t.Fatal("the probe is current: nothing to probe")
			return nil, nil, nil
		},
	}
	_, err = r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(mf)})
	require.NoError(t, err)

	var got catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(mf), &got))
	ready := meta.FindStatusCondition(got.Status.Conditions, catalogv1alpha1.MediaFileConditionReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionTrue, ready.Status)
	assert.Equal(t, "Ready", ready.Reason)
	assert.Equal(t, mf.Status.ProbeHash, got.Status.ProbeHash, "the probe is kept")
}
