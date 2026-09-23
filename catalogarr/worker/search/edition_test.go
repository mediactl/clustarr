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
package search

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// kidA is an Album of Kid A (first released 2000) whose release group, as
// the metadata gateway fills it (ReleaseSummary.ReleaseDate, Z4), holds the
// 2000 CD, an undated vinyl and the 2009 collector's edition.
func kidA(anyReleaseOK *bool, selected string, pinned *string) *catalogv1alpha1.Album {
	date := func(y, m, d int) *metav1.Time {
		// Decoded west of UTC, as metav1.Time is: the year is read in UTC.
		t := metav1.NewTime(time.Date(y, time.Month(m), d, 1, 0, 0, 0, time.UTC).In(time.FixedZone("UTC-5", -5*60*60)))
		return &t
	}
	return &catalogv1alpha1.Album{
		Spec: catalogv1alpha1.AlbumSpec{AnyReleaseOk: anyReleaseOK, ReleaseID: pinned},
		Status: catalogv1alpha1.AlbumStatus{Metadata: &catalogv1alpha1.AlbumMetadata{
			Title: "Kid A", ReleaseDate: date(2000, 10, 2), SelectedReleaseID: selected,
			Releases: []catalogv1alpha1.ReleaseSummary{
				{ID: "cd-2000", ReleaseDate: date(2000, 10, 2)},
				{ID: "vinyl", ReleaseDate: nil},
				{ID: "ce-2009", ReleaseDate: date(2009, 8, 31)},
				{ID: "cd-2000-us", ReleaseDate: date(2000, 10, 3)},
			},
		}},
	}
}

var radiohead = &catalogv1alpha1.Artist{Status: catalogv1alpha1.ArtistStatus{
	Metadata: &catalogv1alpha1.ArtistMetadata{Name: "Radiohead"},
}}

// TestAlbumIdentityCarriesTheEditionYearsTheAlbumAccepts: every dated
// release's year while anyReleaseOk (its default), each once; only the
// selected release's -- or the pinned one's, before a selection -- when it
// is off (Lidarr's `Where(r => r.Monitored || album.AnyReleaseOk)`).
func TestAlbumIdentityCarriesTheEditionYearsTheAlbumAccepts(t *testing.T) {
	require.Equal(t, []int{2000, 2009}, AlbumIdentity(kidA(nil, "cd-2000", nil), radiohead).EditionYears)
	require.Equal(t, []int{2000, 2009}, AlbumIdentity(kidA(ptr.To(true), "", nil), radiohead).EditionYears)
	require.Equal(t, []int{2009}, AlbumIdentity(kidA(ptr.To(false), "ce-2009", nil), radiohead).EditionYears)
	require.Equal(t, []int{2009}, AlbumIdentity(kidA(ptr.To(false), "", ptr.To("ce-2009")), radiohead).EditionYears)
	require.Empty(t, AlbumIdentity(kidA(ptr.To(false), "vinyl", nil), radiohead).EditionYears, "an undated release has no year")
}

// TestARemasterDatedYearsLaterMatchesItsAlbum is the edition year end to
// end: the 2009 collector's edition of a 2000 album is nine years out,
// beyond Lidarr's five-year hard reject, and was WrongItem until the
// identity carried the album's edition years. A year near no edition is
// still refused, and an album that accepts only its 2000 release refuses
// the 2009 one.
func TestARemasterDatedYearsLaterMatchesItsAlbum(t *testing.T) {
	identityRejections := func(a *catalogv1alpha1.Album, title string) []string {
		tg := decision.Target{Kind: commonv1.MediaKindAlbum, Available: true, Identity: AlbumIdentity(a, radiohead)}
		rel := commonv1.ReleaseInfo{GUID: title, IndexerRef: "idx", Title: title, Protocol: commonv1.ProtocolTorrent}
		ds := decision.Evaluate(context.Background(), tg, quality.Profile{}, &catalogue.Catalogue{},
			[]commonv1.ReleaseInfo{rel}, decision.Options{UserInvoked: true, ProtocolsEnabled: map[string]bool{"torrent": true}})
		require.Len(t, ds, 1)
		require.NotNil(t, ds[0].Parsed, title)
		var out []string
		for _, r := range ds[0].Rejections {
			if strings.HasPrefix(r.Reason, decision.ReasonWrongItem.Code+":") || strings.HasPrefix(r.Reason, decision.ReasonUnknownItem.Code+":") {
				out = append(out, r.Reason)
			}
		}
		return out
	}

	require.Empty(t, identityRejections(kidA(nil, "cd-2000", nil), "Radiohead - Kid A (2009) [FLAC]"),
		"the 2009 collector's edition is Kid A")
	require.Empty(t, identityRejections(kidA(nil, "cd-2000", nil), "Radiohead-Kid_A-2CD-FLAC-2010-GRP"),
		"a year either side of an edition")
	require.NotEmpty(t, identityRejections(kidA(nil, "cd-2000", nil), "Radiohead - Kid A (2016) [FLAC]"),
		"near no edition and beyond five years of the album")
	require.NotEmpty(t, identityRejections(kidA(ptr.To(false), "cd-2000", nil), "Radiohead - Kid A (2009) [FLAC]"),
		"an album that accepts only its selected 2000 release refuses the 2009 edition")
}

// TestAMultiTrackAlbumWithFilesHasACurrentFile: an album has a file when the
// Album reconciler has rolled a quality up from its MediaFiles. Its
// trackFileCount counts only files tied to a track -- which the importer
// does for a one-track album alone -- so gating on it read every imported
// multi-track album as empty, and the search grabbed it again.
func TestAMultiTrackAlbumWithFilesHasACurrentFile(t *testing.T) {
	flac := commonv1.Quality{Name: "FLAC"}
	artist := &catalogv1alpha1.Artist{}
	artist.Name, artist.Namespace = "radiohead", "default"
	artist.Status.Metadata = &catalogv1alpha1.ArtistMetadata{Name: "Radiohead"}

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
			album := kidA(nil, "cd-2000", nil)
			album.Name, album.Namespace = "kid-a", "default"
			album.Spec.ArtistRef = artist.Name
			album.Status.TrackFileCount = tc.trackFiles
			album.Status.Quality = tc.quality
			album.Status.FormatScore = 7
			for i := range 10 {
				album.Status.Tracks = append(album.Status.Tracks, catalogv1alpha1.Track{RecordingID: string(rune('a' + i)), Number: int32(i + 1)})
			}
			c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(album, artist).Build()

			v, err := ReadNonVideo(context.Background(), c, "default", commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: album.Name}, time.Now())
			require.NoError(t, err)
			require.Equal(t, tc.want, v.Current)
		})
	}
}
