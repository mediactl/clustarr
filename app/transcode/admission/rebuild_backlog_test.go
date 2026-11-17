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

package admission_test

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/app/transcode/admission"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// nothingInFlight is an APIReader whose quorum Lists find no file.
type nothingInFlight struct{ client.Reader }

func (nothingInFlight) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return nil
}

// backlogReader is the cache the backlog scan reads, recording how.
type backlogReader struct {
	client.Reader
	unfiltered, gets int
}

func (r *backlogReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	lo := client.ListOptions{}
	lo.ApplyOptions(opts)
	if lo.FieldSelector == nil || lo.FieldSelector.Empty() {
		r.unfiltered++
	}
	return r.Reader.List(ctx, list, opts...)
}

func (r *backlogReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	r.gets++
	return r.Reader.Get(ctx, key, obj, opts...)
}

// The backlog scan asks only for the video files, by kind, and reads each
// on its own: it never lists the whole kind (talos, 2026-11-17: a List of
// every MediaFile decoded 33,304 of them out of the row cache at once,
// half of them music tracks no transcode takes, every five minutes).
func TestTheBacklogScanReadsOnlyTheVideoFilesByKind(t *testing.T) {
	file := func(name string, kind commonv1.MediaKind) *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: name, UID: types.UID(name)},
			Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: commonv1.MediaRef{Kind: kind, Name: name + "-item"}},
		}
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithIndex(&catalogv1alpha1.MediaFile{}, mfindex.MediaKind, mfindex.MediaKindValues).
		WithObjects(
			file("a-movie", commonv1.MediaKindMovie),
			file("an-episode", commonv1.MediaKindEpisode),
			file("another-episode", commonv1.MediaKindEpisode),
			file("a-track", commonv1.MediaKindAlbum),
			file("a-book", commonv1.MediaKindBook),
		).Build()
	cache := &backlogReader{Reader: c}

	profile := &transcodev1alpha1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{Name: "hevc"}}
	var asked []string
	l := admission.NewLedger(admission.Options{Slots: map[string]int32{"cpu": 2}, Window: 32})
	require.NoError(t, l.Rebuild(context.Background(), admission.RebuildInputs{
		APIReader: nothingInFlight{}, Cache: cache, Namespaces: []string{"media"},
		Eligible: func(_ context.Context, mf *catalogv1alpha1.MediaFile) *transcodeplan.Eligibility {
			asked = append(asked, mf.Name)
			return &transcodeplan.Eligibility{Eligible: true, Profile: profile, Priority: 50}
		},
	}))

	sort.Strings(asked)
	assert.Equal(t, []string{"a-movie", "an-episode", "another-episode"}, asked, "the video files, and no track or book")
	assert.Zero(t, cache.unfiltered, "never a List of the whole kind")
	assert.Equal(t, 3, cache.gets, "each video file read on its own")
	assert.Equal(t, 3, l.Backlog("hevc"), "and each is in the backlog")
}
