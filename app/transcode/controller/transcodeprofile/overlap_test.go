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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
)

// An overlap verdict stands for overlapTTL, or until a profile's generation
// moves: the reconciler runs at every change of a profile's dispatched
// counts, and each run listed every MediaFile for it (talos, 2026-11-03: 4%
// of the manager's CPU).
func TestAnOverlapVerdictStandsForItsTTL(t *testing.T) {
	s := runtime.NewScheme()
	require.NoError(t, catalogv1alpha1.AddToScheme(s))
	require.NoError(t, transcodev1alpha1.AddToScheme(s))
	sel := &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "small"}}
	low := transcodev1alpha1.TranscodeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "low", Generation: 1},
		Spec:       transcodev1alpha1.TranscodeProfileSpec{Selector: sel, Priority: 1},
	}
	high := transcodev1alpha1.TranscodeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "high", Generation: 1},
		Spec:       transcodev1alpha1.TranscodeProfileSpec{Selector: sel, Priority: 10},
	}
	c := fake.NewClientBuilder().WithScheme(s).Build()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	r := &Reconciler{Client: c, Now: func() time.Time { return now }}
	ctx := context.Background()
	profiles := []transcodev1alpha1.TranscodeProfile{low, high}

	overlapped, err := r.overlap(ctx, &low, profiles)
	require.NoError(t, err)
	assert.False(t, overlapped, "no file is selected by both")

	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "heat", Labels: map[string]string{"tier": "small"}},
		Spec:       catalogv1alpha1.MediaFileSpec{Path: "/data/movies/Heat (1995)/Heat.mkv", MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}},
	}
	require.NoError(t, c.Create(ctx, mf))
	overlapped, err = r.overlap(ctx, &low, profiles)
	require.NoError(t, err)
	assert.False(t, overlapped, "the verdict stands within the TTL")

	now = now.Add(overlapTTL)
	overlapped, err = r.overlap(ctx, &low, profiles)
	require.NoError(t, err)
	assert.True(t, overlapped, "the files are listed again once the TTL is out")

	require.NoError(t, c.Delete(ctx, mf))
	overlapped, err = r.overlap(ctx, &low, profiles)
	require.NoError(t, err)
	assert.True(t, overlapped, "the verdict stands")
	moved := []transcodev1alpha1.TranscodeProfile{low, high}
	moved[1].Generation = 2
	overlapped, err = r.overlap(ctx, &low, moved)
	require.NoError(t, err)
	assert.False(t, overlapped, "a profile's generation moving lists the files at once")
}
