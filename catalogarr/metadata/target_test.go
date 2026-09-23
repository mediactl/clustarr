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

package metadata

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

func TestNewTargetDispatchesEveryKindWithItsOwnMetadata(t *testing.T) {
	cases := []struct {
		kind commonv1.MediaKind
		want client.Object
	}{
		{commonv1.MediaKindMovie, &catalogv1alpha1.Movie{}},
		{commonv1.MediaKindSeries, &catalogv1alpha1.Series{}},
		{commonv1.MediaKindArtist, &catalogv1alpha1.Artist{}},
		{commonv1.MediaKindAlbum, &catalogv1alpha1.Album{}},
		{commonv1.MediaKindAuthor, &catalogv1alpha1.Author{}},
		{commonv1.MediaKindBook, &catalogv1alpha1.Book{}},
		{commonv1.MediaKindAudiobook, &catalogv1alpha1.Audiobook{}},
		{commonv1.MediaKindComic, &catalogv1alpha1.Comic{}},
	}
	for _, tc := range cases {
		got, err := newTarget(tc.kind)
		require.NoError(t, err, tc.kind)
		require.IsType(t, tc.want, got, tc.kind)
	}
}

// TestNewTargetRejectsEpisodeAndIssue pins the deliberate exclusion this
// worker's errUnsupportedKind doc comment explains: neither kind has a
// status.metadata field or a MetadataReady condition of its own (see
// api/catalog/v1alpha1's episode_types.go and issue_types.go), so this
// worker -- the sole writer of status.metadata under
// k8s.ManagerCatalogarrMetadata -- must never be asked to fetch either.
// Their fields come from their parent's fan-out instead
// (ManagerCatalogarrSeries for Episode; ManagerCatalogarrFanout for Issue,
// task G2-2).
func TestNewTargetRejectsEpisodeAndIssue(t *testing.T) {
	_, err := newTarget(commonv1.MediaKindEpisode)
	require.True(t, errors.Is(err, errUnsupportedKind))

	_, err = newTarget(commonv1.MediaKindIssue)
	require.True(t, errors.Is(err, errUnsupportedKind))
}

func TestExternalIDsReadsTheSpecID(t *testing.T) {
	movie := &catalogv1alpha1.Movie{Spec: catalogv1alpha1.MovieSpec{TmdbID: 27205}}
	ids, err := externalIDs(movie)
	require.NoError(t, err)
	require.Equal(t, pkgmetadata.ExternalIDs{pkgmetadata.KeyTMDB: "27205"}, ids)

	series := &catalogv1alpha1.Series{Spec: catalogv1alpha1.SeriesSpec{TvdbID: 121361}}
	ids, err = externalIDs(series)
	require.NoError(t, err)
	require.Equal(t, pkgmetadata.ExternalIDs{pkgmetadata.KeyTVDB: "121361"}, ids)

	artist := &catalogv1alpha1.Artist{Spec: catalogv1alpha1.ArtistSpec{MusicBrainzID: "5b11f4ce-a62d-471e-81fc-a69a8278c7da"}}
	ids, err = externalIDs(artist)
	require.NoError(t, err)
	require.Equal(t, pkgmetadata.ExternalIDs{pkgmetadata.KeyMBArtist: "5b11f4ce-a62d-471e-81fc-a69a8278c7da"}, ids)

	album := &catalogv1alpha1.Album{Spec: catalogv1alpha1.AlbumSpec{ReleaseGroupID: "f5093c06-23e3-404f-aeaa-40f72885ee3a"}}
	ids, err = externalIDs(album)
	require.NoError(t, err)
	require.Equal(t, pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: "f5093c06-23e3-404f-aeaa-40f72885ee3a"}, ids)

	author := &catalogv1alpha1.Author{Spec: catalogv1alpha1.AuthorSpec{OpenLibraryID: "OL23919A"}}
	ids, err = externalIDs(author)
	require.NoError(t, err)
	require.Equal(t, pkgmetadata.ExternalIDs{pkgmetadata.KeyOpenLibraryAuthor: "OL23919A"}, ids)

	book := &catalogv1alpha1.Book{Spec: catalogv1alpha1.BookSpec{WorkID: "OL45883W"}}
	ids, err = externalIDs(book)
	require.NoError(t, err)
	require.Equal(t, pkgmetadata.ExternalIDs{pkgmetadata.KeyOpenLibraryWork: "OL45883W"}, ids)

	comic := &catalogv1alpha1.Comic{Spec: catalogv1alpha1.ComicSpec{Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-12345"}}
	ids, err = externalIDs(comic)
	require.NoError(t, err)
	require.Equal(t, pkgmetadata.ExternalIDs{pkgmetadata.KeyComicVine: "4050-12345"}, ids)

	manga := &catalogv1alpha1.Comic{Spec: catalogv1alpha1.ComicSpec{Source: catalogv1alpha1.ComicSourceMangaDex, SourceID: "801513ba-a712-498c-8f57-cae55b38cc92"}}
	ids, err = externalIDs(manga)
	require.NoError(t, err)
	require.Equal(t, pkgmetadata.ExternalIDs{"mangadex": "801513ba-a712-498c-8f57-cae55b38cc92"}, ids,
		"a MangaDex comic's UUID is keyed by its own source, never filed under comicvine")
}

// TestExternalIDsAudiobookThreadsRegionThroughTheIDsMap pins the one kind
// where externalIDs carries a second, non-id parameter: Registry.Lookup's
// signature is fixed at (kind, ExternalIDs), so AudiobookSpec.Region rides
// along in ids["region"] the same way rpc.go's lookupEpisodes threads an
// episode order through ids["order"]. An unset Region (the Go zero value,
// even though the CRD defaults it server-side) falls back to "us" so this
// function never sends registry.go's Lookup an empty region string.
func TestExternalIDsAudiobookThreadsRegionThroughTheIDsMap(t *testing.T) {
	withRegion := &catalogv1alpha1.Audiobook{Spec: catalogv1alpha1.AudiobookSpec{
		ASIN: "B002V5BM26", Region: catalogv1alpha1.AudiobookRegionUK,
	}}
	ids, err := externalIDs(withRegion)
	require.NoError(t, err)
	require.Equal(t, pkgmetadata.ExternalIDs{pkgmetadata.KeyASIN: "B002V5BM26", "region": "uk"}, ids)

	noRegion := &catalogv1alpha1.Audiobook{Spec: catalogv1alpha1.AudiobookSpec{ASIN: "B002V5BM26"}}
	ids, err = externalIDs(noRegion)
	require.NoError(t, err)
	require.Equal(t, pkgmetadata.ExternalIDs{pkgmetadata.KeyASIN: "B002V5BM26", "region": "us"}, ids)
}

func TestRefreshedAtIsZeroBeforeTheFirstFetch(t *testing.T) {
	require.True(t, refreshedAt(&catalogv1alpha1.Movie{}).IsZero())

	when := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	m := &catalogv1alpha1.Movie{Status: catalogv1alpha1.MovieStatus{
		Metadata: &catalogv1alpha1.MovieMetadata{RefreshedAt: when},
	}}
	require.True(t, refreshedAt(m).Equal(when.Time))
}

// TestRefreshedAtCoversEveryNewKind spot-checks refreshedAt's six new cases
// (Artist, Album, Author, Book, Audiobook, Comic): each reads
// status.metadata.refreshedAt exactly like Movie and Series above, so one
// representative per kind is enough to prove the switch is wired, not a
// full field-by-field repeat of TestRefreshedAtIsZeroBeforeTheFirstFetch.
func TestRefreshedAtCoversEveryNewKind(t *testing.T) {
	when := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	require.True(t, refreshedAt(&catalogv1alpha1.Artist{}).IsZero())
	require.True(t, refreshedAt(&catalogv1alpha1.Artist{Status: catalogv1alpha1.ArtistStatus{
		Metadata: &catalogv1alpha1.ArtistMetadata{RefreshedAt: when},
	}}).Equal(when.Time))

	require.True(t, refreshedAt(&catalogv1alpha1.Album{Status: catalogv1alpha1.AlbumStatus{
		Metadata: &catalogv1alpha1.AlbumMetadata{RefreshedAt: when},
	}}).Equal(when.Time))

	require.True(t, refreshedAt(&catalogv1alpha1.Author{Status: catalogv1alpha1.AuthorStatus{
		Metadata: &catalogv1alpha1.AuthorMetadata{RefreshedAt: when},
	}}).Equal(when.Time))

	require.True(t, refreshedAt(&catalogv1alpha1.Book{Status: catalogv1alpha1.BookStatus{
		Metadata: &catalogv1alpha1.BookMetadata{RefreshedAt: when},
	}}).Equal(when.Time))

	require.True(t, refreshedAt(&catalogv1alpha1.Audiobook{Status: catalogv1alpha1.AudiobookStatus{
		Metadata: &catalogv1alpha1.AudiobookMetadata{RefreshedAt: when},
	}}).Equal(when.Time))

	require.True(t, refreshedAt(&catalogv1alpha1.Comic{Status: catalogv1alpha1.ComicStatus{
		Metadata: &catalogv1alpha1.ComicMetadata{RefreshedAt: when},
	}}).Equal(when.Time))
}
