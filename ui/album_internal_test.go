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

package ui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/views"
)

// albumStatus is the episode's status cell for an album, the first case
// that applies: a grab in flight, a grab waiting out its delay, the file's
// quality with the track summary -- amber with tracks missing, below the
// cutoff or with no profile -- unreleased, unmonitored, else missing; an
// undated album reads missing.
func TestAlbumStatus(t *testing.T) {
	now := time.Date(2026, time.November, 10, 12, 0, 0, 0, time.UTC)
	past := metav1.NewTime(now.AddDate(-1, 0, 0))
	future := metav1.NewTime(now.AddDate(1, 0, 0))
	flac := &commonv1.Quality{Name: "FLAC"}
	for name, tc := range map[string]struct {
		status    catalogv1.AlbumStatus
		monitored bool
		want      views.RowStatus
	}{
		"downloading": {
			catalogv1.AlbumStatus{Phase: catalogv1.AlbumPhaseDownloading}, true,
			views.RowStatus{Kind: "downloading", Label: "Downloading", Title: "Album is downloading"},
		},
		"a grab in flight whatever the phase": {
			catalogv1.AlbumStatus{Phase: catalogv1.AlbumPhaseWanted, ActiveDownloadRef: new("dl")}, true,
			views.RowStatus{Kind: "downloading", Label: "Downloading", Title: "Album is downloading"},
		},
		"pending": {
			catalogv1.AlbumStatus{Phase: catalogv1.AlbumPhaseDelayed}, true,
			views.RowStatus{Kind: "pending", Label: "Pending", Title: "A release waits out its delay profile"},
		},
		"on disk at the cutoff": {
			catalogv1.AlbumStatus{Phase: catalogv1.AlbumPhaseImported, HasFile: true, FileQuality: flac, TrackSummary: "10/10", CutoffReason: "CutoffMet"}, true,
			views.RowStatus{Kind: "file", Label: "FLAC · 10/10", Title: "Album on disk"},
		},
		"on disk with tracks missing": {
			catalogv1.AlbumStatus{Phase: catalogv1.AlbumPhaseCutoffUnmet, HasFile: true, FileQuality: flac, TrackSummary: "9/10", CutoffReason: "TracksMissing"}, true,
			views.RowStatus{Kind: "file", Label: "FLAC · 9/10", Title: "Tracks are missing: 9/10 on disk", Warn: true},
		},
		"on disk below the cutoff": {
			catalogv1.AlbumStatus{Phase: catalogv1.AlbumPhaseCutoffUnmet, HasFile: true, FileQuality: &commonv1.Quality{Name: "MP3-320"}, CutoffReason: "CutoffUnmet"}, true,
			views.RowStatus{Kind: "file", Label: "MP3-320", Title: "Quality cutoff has not been met", Warn: true},
		},
		"on disk, profile unresolved": {
			catalogv1.AlbumStatus{Phase: catalogv1.AlbumPhaseCutoffUnevaluated, TrackFileCount: 3, FileQuality: flac}, true,
			views.RowStatus{Kind: "file", Label: "FLAC", Title: "Quality profile could not be resolved", Warn: true},
		},
		"on disk, quality unknown": {
			catalogv1.AlbumStatus{Phase: catalogv1.AlbumPhaseImported, HasFile: true}, true,
			views.RowStatus{Kind: "file", Label: "Unknown", Title: "Album on disk"},
		},
		"unreleased": {
			catalogv1.AlbumStatus{Phase: catalogv1.AlbumPhaseWanted, Metadata: &catalogv1.AlbumMetadata{ReleaseDate: &future}}, true,
			views.RowStatus{Kind: "unreleased", Label: "Unreleased", Title: "Album has not been released"},
		},
		"unmonitored": {
			catalogv1.AlbumStatus{Phase: catalogv1.AlbumPhaseUnmonitored, Metadata: &catalogv1.AlbumMetadata{ReleaseDate: &past}}, false,
			views.RowStatus{Kind: "unmonitored", Label: "Unmonitored", Title: "Album is not monitored"},
		},
		"missing": {
			catalogv1.AlbumStatus{Phase: catalogv1.AlbumPhaseWanted, Metadata: &catalogv1.AlbumMetadata{ReleaseDate: &past}}, true,
			views.RowStatus{Kind: "missing", Label: "Missing", Title: "Album missing from disk"},
		},
		"undated reads missing": {
			catalogv1.AlbumStatus{Phase: catalogv1.AlbumPhaseWanted}, true,
			views.RowStatus{Kind: "missing", Label: "Missing", Title: "Album missing from disk"},
		},
	} {
		a := &catalogv1.Album{Status: tc.status}
		assert.Equal(t, tc.want, albumStatus(a, tc.monitored, now), name)
	}
}

// albumTableRow reads the type lower-cased, as the groups key it, and the
// release date as the table shows it.
func TestAlbumTableRowReadsTheMetadata(t *testing.T) {
	now := time.Date(2026, time.November, 10, 12, 0, 0, 0, time.UTC)
	released := metav1.NewTime(time.Date(1973, time.March, 1, 0, 0, 0, 0, time.UTC))
	a := &catalogv1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "dsotm", Namespace: "default"},
		Spec:       catalogv1.AlbumSpec{ArtistRef: "pink-floyd"},
		Status: catalogv1.AlbumStatus{
			Phase: catalogv1.AlbumPhaseWanted, TrackSummary: "0/10", CutoffReason: "Pending",
			Metadata: &catalogv1.AlbumMetadata{Title: "The Dark Side of the Moon", AlbumType: "Album", SecondaryTypes: []string{"Live"}, ReleaseDate: &released, Disambiguation: "remaster"},
		},
	}
	row := albumTableRow(a, now)
	assert.Equal(t, "album", row.Type)
	assert.Equal(t, "Album", row.TypeLabel())
	assert.Equal(t, []string{"Live"}, row.SecondaryTypes)
	assert.Equal(t, "1973-03-01", row.ReleaseDate)
	assert.Equal(t, "Mar 1 1973", row.ReleaseDateLabel)
	assert.Equal(t, "remaster", row.Disambiguation)
	assert.Equal(t, "pink-floyd", row.Artist)
	assert.Equal(t, "/library/default/artist/pink-floyd", row.ReturnURL())
	assert.True(t, row.Monitored, "nil spec.monitored is the CRD default, true")
	assert.Equal(t, "missing", row.Status.Kind)

	bare := albumTableRow(&catalogv1.Album{ObjectMeta: metav1.ObjectMeta{Name: "x"}}, now)
	assert.Equal(t, "x", bare.DisplayTitle(), "the object name before the metadata")
	assert.Empty(t, bare.TypeLabel())
}

// The artist hero's facts: the rollup's summary, the years active and the
// country's name.
func TestArtistFacts(t *testing.T) {
	a := &catalogv1.Artist{Status: catalogv1.ArtistStatus{AlbumCount: 12, ContainerRollup: catalogv1.ContainerRollup{MissingChildCount: 3, DownloadingChildCount: 1, CutoffUnmetChildCount: 2}}}
	assert.Equal(t, "12 albums, 3 missing, 1 downloading, 2 below cutoff", artistSummary(a, 0))
	assert.Equal(t, "1 album", artistSummary(&catalogv1.Artist{Status: catalogv1.ArtistStatus{AlbumCount: 1}}, 0))
	assert.Equal(t, "5 albums", artistSummary(&catalogv1.Artist{}, 5), "the listed count before the rollup")
	assert.Equal(t, "No albums yet", artistSummary(&catalogv1.Artist{}, 0))

	for name, tc := range map[string]struct {
		begin, end string
		status     catalogv1.ArtistRunStatus
		want       string
	}{
		"ended":                {"1965", "2014-11-07", catalogv1.ArtistRunStatusEnded, "1965 – 2014"},
		"continuing":           {"1990-02-23", "", catalogv1.ArtistRunStatusContinuing, "1990 – present"},
		"status unknown":       {"1990", "", "", "1990 – present"},
		"ended without an end": {"1965", "", catalogv1.ArtistRunStatusEnded, "1965"},
		"an end alone":         {"", "2014", catalogv1.ArtistRunStatusEnded, "– 2014"},
		"nothing":              {"", "", catalogv1.ArtistRunStatusEnded, ""},
	} {
		assert.Equal(t, tc.want, yearsActive(tc.begin, tc.end, tc.status), name)
	}

	assert.Equal(t, "United Kingdom", countryName("GB"))
	assert.Equal(t, "United States", countryName("US"))
	assert.Equal(t, "", countryName(""))
	assert.Equal(t, "nowhere", countryName("nowhere"), "a code x/text does not know is shown as it is")

	links := artistLinks(&catalogv1.Artist{
		Spec: catalogv1.ArtistSpec{MusicBrainzID: "mbid"},
		Status: catalogv1.ArtistStatus{Metadata: &catalogv1.ArtistMetadata{Links: []catalogv1.Link{
			{Type: "wikidata", URL: "https://www.wikidata.org/wiki/Q1"},
			{Type: "discogs", URL: "https://www.discogs.com/artist/1"},
			{Type: "lastfm", URL: "https://www.last.fm/music/x"},
			{Type: "homepage", URL: ""},
		}}},
	})
	assert.Equal(t, []views.Link{
		{Label: "MusicBrainz", Href: "https://musicbrainz.org/artist/mbid", Provider: views.ProviderMusicBrainz},
		{Label: "Discogs", Href: "https://www.discogs.com/artist/1", Provider: views.ProviderDiscogs},
		{Label: "Last.fm", Href: "https://www.last.fm/music/x"},
	}, links, "Wikidata and an empty link are left out")
}

// replyAlbumRow answers a toggle with the artist page's row: from the
// patched Album on success, from the Album as it stands with the failure
// inside it otherwise, and a bare row when there is nothing to read.
func TestReplyAlbumRow(t *testing.T) {
	released := metav1.NewTime(time.Date(1973, time.March, 1, 0, 0, 0, 0, time.UTC))
	current := &catalogv1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "dsotm", Namespace: "default"},
		Spec:       catalogv1.AlbumSpec{ArtistRef: "pink-floyd", ReleaseGroupID: "rg", Monitored: new(true)},
		Status:     catalogv1.AlbumStatus{Phase: catalogv1.AlbumPhaseWanted, Metadata: &catalogv1.AlbumMetadata{Title: "The Dark Side of the Moon", ReleaseDate: &released}},
	}
	scheme, err := NewReaderScheme()
	require.NoError(t, err)
	srv := NewServer(t.Context(), Options{Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(current).Build()})

	patched := current.DeepCopy()
	patched.Spec.Monitored = new(false)
	rec := httptest.NewRecorder()
	srv.replyAlbumRow(rec, httptest.NewRequest(http.MethodPost, "/library/default/album/dsotm/monitor", nil), "default", "dsotm", patched, nil)
	body := rec.Body.String()
	assert.Contains(t, body, `data-album="dsotm"`)
	assert.Contains(t, body, `data-monitored="false"`, "the row reads the Album the patch returned")
	assert.Contains(t, body, `data-status="unmonitored"`)
	assert.NotContains(t, body, `data-action-error`)

	rec = httptest.NewRecorder()
	srv.replyAlbumRow(rec, httptest.NewRequest(http.MethodPost, "/library/default/album/dsotm/monitor", nil), "default", "dsotm", nil, errors.New("boom"))
	body = rec.Body.String()
	assert.Contains(t, body, `data-monitored="true"`, "a failure renders the Album as it still stands")
	assert.Contains(t, body, `data-action-error="failed"`)
	assert.Contains(t, body, "The Dark Side of the Moon")

	rec = httptest.NewRecorder()
	srv.replyAlbumRow(rec, httptest.NewRequest(http.MethodPost, "/library/default/album/gone/monitor", nil), "default", "gone", nil, errors.New("boom"))
	assert.Contains(t, rec.Body.String(), `data-album="gone"`, "a bare row when there is nothing to read")
}

// durationLabel sums the tracks and rounds to the minute, as the hero says
// it.
func TestDurationLabel(t *testing.T) {
	assert.Equal(t, "", durationLabel(nil))
	assert.Equal(t, "1m", durationLabel([]catalogv1.Track{{DurationMs: 20_000}}), "under a minute reads one")
	assert.Equal(t, "8m", durationLabel([]catalogv1.Track{{DurationMs: 90_000}, {DurationMs: 163_000}, {DurationMs: 210_000}}))
	assert.Equal(t, "1h 12m", durationLabel([]catalogv1.Track{{DurationMs: 72 * 60_000}}))
}
