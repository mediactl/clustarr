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

package itemread_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/itemread"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestCurrentReadsTheMediaFile: an item's current file is its MediaFile's
// frozen spec -- revision and source included -- not the item's rolled-up
// quality, which has neither. Since ADR-0019 §6.2 the source's info hash is
// frozen on the MediaFile too (spec.importedFrom.infoHash), where it used to
// be read off the importing Download. (Re-homed from the RSS matcher's
// TestCurrentFileReadsTheMediaFile: A4.4 left the matcher no current file;
// the owner's grab planner and the Search controller read it here.)
func TestCurrentReadsTheMediaFile(t *testing.T) {
	ctx := context.Background()
	const ns = "rss"
	web1080 := commonv1.Quality{Name: "WEBDL-1080p", Source: commonv1.SourceWebDL, Resolution: 1080}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(
		&catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: "file", Namespace: ns},
			Spec: catalogv1alpha1.MediaFileSpec{
				Path: "/data/tv/x.mkv", Quality: web1080, Revision: commonv1.Revision{Version: 2},
				FormatScore: 30, MatchedFormats: []string{"x265"},
				ImportedFrom: &catalogv1alpha1.ImportSource{
					DownloadRef: "dl", ReleaseTitle: "Show.S01E01.1080p.WEB-DL.PROPER-GRP", InfoHash: "abc123",
				},
			},
		},
	).Build()
	movie := func(hasFile bool, ref *string) *catalogv1alpha1.Movie {
		return &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: ns},
			Status:     catalogv1alpha1.MovieStatus{HasFile: hasFile, FileRef: ref},
		}
	}

	cur, err := itemread.Current(ctx, c, movie(true, new("file")))
	require.NoError(t, err)
	require.NotNil(t, cur)
	assert.Equal(t, web1080, cur.Quality)
	assert.Equal(t, commonv1.Revision{Version: 2}, cur.Revision, "the revision is what makes a PROPER an upgrade, or not")
	assert.Equal(t, 30, cur.FormatScore)
	assert.Equal(t, []string{"x265"}, cur.Formats)
	assert.Equal(t, "Show.S01E01.1080p.WEB-DL.PROPER-GRP", cur.SourceTitle)
	assert.Equal(t, "abc123", cur.SourceHash, "the info hash frozen at import")

	for name, c2 := range map[string]struct {
		hasFile bool
		ref     *string
	}{
		"no file":                       {false, new("file")},
		"a file with no ref":            {true, nil},
		"a ref to a MediaFile now gone": {true, new("gone")},
	} {
		cur, err := itemread.Current(ctx, c, movie(c2.hasFile, c2.ref))
		require.NoError(t, err, name)
		assert.Nil(t, cur, name)
	}
}

// TestAMultiTrackAlbumWithFilesHasACurrentFile: an album has a file when the
// Album reconciler has rolled a quality up from its MediaFiles. Its
// trackFileCount counts only files tied to a track -- which the importer
// does for a one-track album alone -- so gating on it read every imported
// multi-track album as empty, and the search grabbed it again. (Re-homed
// from the search worker's snapshot, which no longer reads a current file:
// the owner's grab planner judges against itemread.Current.)
func TestAMultiTrackAlbumWithFilesHasACurrentFile(t *testing.T) {
	flac := commonv1.Quality{Name: "FLAC"}
	for name, tc := range map[string]struct {
		trackFiles int32
		quality    *commonv1.Quality
		want       *decision.Current
	}{
		"ten tracks, files imported, none tied to a track": {trackFiles: 0, quality: &flac, want: &decision.Current{Quality: flac, FormatScore: 7}},
		"ten tracks, every one tied":                       {trackFiles: 10, quality: &flac, want: &decision.Current{Quality: flac, FormatScore: 7}},
		"no file":                                          {trackFiles: 0, quality: nil, want: nil},
	} {
		t.Run(name, func(t *testing.T) {
			album := &catalogv1alpha1.Album{ObjectMeta: metav1.ObjectMeta{Name: "kid-a", Namespace: "default"}}
			album.Spec.ArtistRef = "radiohead"
			album.Status.TrackFileCount = tc.trackFiles
			album.Status.Quality = tc.quality
			album.Status.FormatScore = 7
			for i := range 10 {
				album.Status.Tracks = append(album.Status.Tracks, catalogv1alpha1.Track{RecordingID: string(rune('a' + i)), Number: int32(i + 1)})
			}
			c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(album).Build()

			got, err := itemread.Current(context.Background(), c, album)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// An upgradeable transcode is its release to the decision: not transcoded,
// so TranscodedFinal never refuses its upgrade, and no codec or HDR
// baseline, since its HEVC is the transcode's own -- the AVC Blu-ray that
// replaces a WEB-DL transcode is the point (the owner, 2026-10-27, The
// Sopranos). A final transcode keeps both.
func TestAnUpgradeableTranscodeIsItsRelease(t *testing.T) {
	web1080 := commonv1.Quality{Name: "WEBDL-1080p", Source: commonv1.SourceWebDL, Resolution: 1080}
	transcode := func(annotations map[string]string) *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Annotations: annotations},
			Spec:       catalogv1alpha1.MediaFileSpec{Quality: web1080, Original: new(false)},
			Status:     catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{VideoCodec: "hevc", Hdr: commonv1.HdrFormatNone}},
		}
	}
	final := itemread.CurrentOf(transcode(nil))
	assert.True(t, final.Transcoded)
	assert.Equal(t, "hevc", final.VideoCodec)

	up := itemread.CurrentOf(transcode(map[string]string{catalogv1alpha1.AnnotationUpgradeable: "true"}))
	assert.False(t, up.Transcoded)
	assert.Empty(t, up.VideoCodec)
	assert.Empty(t, up.HDR)
	assert.Equal(t, web1080, up.Quality)
}

// The decision's current file carries the bitrate the profile's range
// judges it by (MediaFile.BytesPerSecond: the source's when recorded).
func TestTheCurrentFileCarriesItsBitrate(t *testing.T) {
	mf := &catalogv1alpha1.MediaFile{
		Spec:   catalogv1alpha1.MediaFileSpec{SizeBytes: 175_055_649, SourceSizeBytes: 1_032_000_000},
		Status: catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{RuntimeMillis: 2_580_000}},
	}
	assert.Equal(t, int64(400_000), itemread.CurrentOf(mf).BytesPerSecond)
}
