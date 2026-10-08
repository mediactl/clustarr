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

package mediafilestatus_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation/mediafilestatus"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestAC1ControlCharacterNeverBlocksTheStatusApply is
// TestRenderClampsEveryBoundedString's envtest half: a status carrying
// U+0096 in a probe title and three block messages is refused whole (the
// falsification leg), and the same status through Render applies.
func TestAC1ControlCharacterNeverBlocksTheStatusApply(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	ctx := context.Background()

	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "default"},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "c1"},
			Path:     "/data/media/movies/c1.mkv", Quality: commonv1.Quality{Name: "Bluray-1080p"},
		},
	}
	require.NoError(t, c.Create(ctx, mf))
	draft := &catalogv1alpha1.MediaFileStatus{
		ProbeHash: "h1",
		MediaInfo: &commonv1.MediaInfo{Container: "mkv", Audio: []commonv1.AudioStream{{Index: 1, Codec: "aac", Language: "eng", Title: "Commentary \u0096 track"}}},
		Transcode: &catalogv1alpha1.TranscodeState{Phase: catalogv1alpha1.TranscodePhaseFailed, Reason: "InvalidSource", Message: "ffgo: \u0096 at byte 9", Blocked: true},
		Subtitles: &catalogv1alpha1.SubtitlesStatus{Phase: catalogv1alpha1.SubtitlesPhaseBlocked, Reason: "MediaDirUnreadable", Message: "open /data/\u0096: permission denied"},
		Graft:     &catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseFailed, Reason: "AlignmentRejected", Message: "donor \u0096 rejected"},
	}
	apply := func(st catalogv1alpha1.MediaFileStatus) error {
		raw, err := json.Marshal(st)
		require.NoError(t, err)
		var ac catalogac.MediaFileStatusApplyConfiguration
		require.NoError(t, json.Unmarshal(raw, &ac))
		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalog, catalogac.MediaFile("c1", "default").WithStatus(&ac))
		return err
	}
	err = apply(*draft)
	require.Error(t, err, "the falsification leg: a raw U+0096 must be refused, or this test proves nothing")
	t.Logf("raw apply refused: %v", err)

	rendered, dropped := mediafilestatus.Render(nil, draft)
	assert.Empty(t, dropped)
	require.NoError(t, apply(rendered), "the rendered status applies")
	var got catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(mf), &got))
	require.NotNil(t, got.Status.MediaInfo)
	assert.Equal(t, "Commentary � track", got.Status.MediaInfo.Audio[0].Title)
	require.NotNil(t, got.Status.Transcode)
	assert.Equal(t, "ffgo: � at byte 9", got.Status.Transcode.Message)
	require.NotNil(t, got.Status.Subtitles)
	assert.Equal(t, "open /data/�: permission denied", got.Status.Subtitles.Message)
	require.NotNil(t, got.Status.Graft)
	assert.Equal(t, "donor � rejected", got.Status.Graft.Message)
	assert.Equal(t, "h1", got.Status.ProbeHash)
}
