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

package ui_test

import (
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// musicFixture is an artist with albums of every type and in every state
// the status cell distinguishes -- on disk with tracks missing,
// downloading, pending, missing, unmonitored, unreleased, undated -- two
// of one album's track files, another artist's album, and two Searches: one
// answered for an album with music results, one for another item. The fake
// client is the reader and the writer, with W4a's album index.
func musicFixture(t *testing.T) (*ui.Server, client.Client) {
	t.Helper()
	date := func(y int, m time.Month, d int) *metav1.Time {
		tm := metav1.NewTime(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
		return &tm
	}
	future := metav1.NewTime(time.Now().AddDate(1, 0, 0))
	artist := &catalogv1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "pink-floyd", Namespace: "default", UID: "pf-uid"},
		Spec:       catalogv1.ArtistSpec{MusicBrainzID: "83d91898-7763-47d7-b03b-b92132375c47", QualityProfileRef: "music-lossless", RootFolderRef: "music"},
		Status: catalogv1.ArtistStatus{
			Path:            "/data/music/Pink Floyd",
			AlbumCount:      4,
			ContainerRollup: catalogv1.ContainerRollup{Phase: catalogv1.ContainerPhaseReady, MissingChildCount: 1, CutoffUnmetChildCount: 1},
			Metadata: &catalogv1.ArtistMetadata{
				Name: "Pink Floyd", SortName: "Floyd, Pink", Disambiguation: "UK rock band", Type: "Group",
				Country: "GB", Begin: "1965", End: "2014-11-07", Status: catalogv1.ArtistRunStatusEnded,
				Genres: []string{"Progressive Rock", "Psychedelic Rock"}, Overview: "Pink Floyd were an English rock band.",
				Links: []catalogv1.Link{
					{Type: "discogs", URL: "https://www.discogs.com/artist/45467"},
					{Type: "wikipedia", URL: "https://en.wikipedia.org/wiki/Pink_Floyd"},
					{Type: "bandcamp", URL: "https://pinkfloyd.bandcamp.com/"},
					{Type: "wikidata", URL: "https://www.wikidata.org/wiki/Q2306"},
				},
			},
			Artwork: []catalogv1.ArtworkEntry{{
				Type: catalogv1.ImageTypeFanart, Source: catalogv1.ArtworkSourceProvider,
				SourceURL: "https://assets.fanart.tv/pf.jpg", Digest: "pf-fanart", SizeBytes: 1, UpdatedAt: metav1.Now(),
			}},
		},
	}
	album := func(name, artistRef, title, albumType string, released *metav1.Time, status catalogv1.AlbumStatus) *catalogv1.Album {
		status.Metadata = &catalogv1.AlbumMetadata{Title: title, AlbumType: albumType, ReleaseDate: released}
		return &catalogv1.Album{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(name + "-uid")},
			Spec:       catalogv1.AlbumSpec{ArtistRef: artistRef, ReleaseGroupID: "rg-" + name, Monitored: new(true)},
			Status:     status,
		}
	}
	dsotm := album("dsotm", "pink-floyd", "The Dark Side of the Moon", "Album", date(1973, time.March, 1), catalogv1.AlbumStatus{
		HasFile: true, TrackFileCount: 2, TrackSummary: "2/3", FileQuality: &commonv1.Quality{Name: "FLAC"},
		Phase: catalogv1.AlbumPhaseCutoffUnmet, CutoffReason: "TracksMissing",
		Path: "/data/music/Pink Floyd/The Dark Side of the Moon (1973)",
		Tracks: []catalogv1.Track{
			{RecordingID: "rec-3", Medium: 1, Number: 3, AbsoluteNumber: 3, Title: "On the Run", DurationMs: 210_000},
			{RecordingID: "rec-1", Medium: 1, Number: 1, AbsoluteNumber: 1, Title: "Speak to Me", DurationMs: 90_000, FileRef: new("dsotm-01")},
			{RecordingID: "rec-2", Medium: 1, Number: 2, AbsoluteNumber: 2, Title: "Breathe", DurationMs: 163_000, FileRef: new("dsotm-02"), Explicit: true},
		},
		Artwork: []catalogv1.ArtworkEntry{{
			Type: catalogv1.ImageTypePoster, Source: catalogv1.ArtworkSourceProvider,
			SourceURL: "https://coverartarchive.org/release/x/front.jpg", Digest: "dsotm-cover", SizeBytes: 1, UpdatedAt: metav1.Now(),
		}},
	})
	dsotm.Status.Metadata.Label = "Harvest"
	dsotm.Status.Metadata.SelectedReleaseID = "rel-1"
	dsotm.Status.Metadata.ExternalIDs = map[string]string{"discogs": "10362"}
	dsotm.Status.Metadata.ArtistCredit = "Pink Floyd"
	dsotm.Status.Metadata.Overview = "The eighth studio album by Pink Floyd."
	dsotm.Status.Metadata.Genres = []string{"Progressive Rock"}
	live := album("animals-live", "pink-floyd", "Animals Live", "Album", date(1977, time.January, 23), catalogv1.AlbumStatus{
		Phase: catalogv1.AlbumPhaseDownloading, ActiveDownloadRef: new("dl"),
	})
	live.Status.Metadata.SecondaryTypes = []string{"Live"}
	ep := album("early-ep", "pink-floyd", "The Early Years", "EP", date(1980, time.January, 1), catalogv1.AlbumStatus{Phase: catalogv1.AlbumPhaseUnmonitored})
	ep.Spec.Monitored = new(false)
	file := func(name, path string, bitrate int32) *catalogv1.MediaFile {
		return &catalogv1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: catalogv1.MediaFileSpec{
				MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "dsotm"},
				Path:     "/data/music/Pink Floyd/The Dark Side of the Moon (1973)/" + path, SizeBytes: 30 << 20,
				Quality: commonv1.Quality{Name: "FLAC"}, ReleaseGroup: "GRP", FormatScore: 10,
			},
			Status: catalogv1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{
				Audio: []commonv1.AudioStream{{Codec: "flac", Channels: 2, BitrateKbps: bitrate, SampleRateHz: 44100, BitDepth: 16}},
			}},
		}
	}
	answered := &catalogv1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "wish-answered", Namespace: "default"},
		Spec:       catalogv1.SearchSpec{MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "wish"}},
		Status: catalogv1.SearchStatus{
			Phase:           catalogv1.SearchPhaseCompleted,
			IndexerOutcomes: []catalogv1.IndexerOutcome{{Name: "nzbgeek", State: catalogv1.IndexerOutcomeOK, Count: 2}},
			Results: []commonv1.ReleaseDecision{
				musicRelease("guid-mp3", 2, "Pink Floyd - Wish You Were Here (1975) [MP3-320]", "MP3-320",
					commonv1.Rejection{Reason: "Quality: MP3-320 is not wanted in profile", Type: commonv1.RejectionPermanent}),
				musicRelease("guid-flac", 1, "Pink Floyd - Wish You Were Here (1975) [FLAC 24bit]", "FLAC 24bit"),
			},
		},
	}
	other := &catalogv1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "andor-s01e02-abc12", Namespace: "default"},
		Spec:       catalogv1.SearchSpec{MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "andor-s01e02"}},
		Status:     catalogv1.SearchStatus{Phase: catalogv1.SearchPhaseCompleted},
	}
	c := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(artist, dsotm, live, ep, answered, other,
		file("dsotm-01", "01 - Speak to Me.flac", 1411), file("dsotm-02", "02 - Breathe.flac", 1020),
		album("wish", "pink-floyd", "Wish You Were Here", "Album", date(1975, time.September, 12), catalogv1.AlbumStatus{Phase: catalogv1.AlbumPhaseWanted}),
		album("money-single", "pink-floyd", "Money", "Single", date(1973, time.May, 7), catalogv1.AlbumStatus{Phase: catalogv1.AlbumPhaseDelayed}),
		album("forthcoming", "pink-floyd", "Forthcoming", "Album", &future, catalogv1.AlbumStatus{Phase: catalogv1.AlbumPhaseWanted}),
		album("undated", "pink-floyd", "Undated Sessions", "Album", nil, catalogv1.AlbumStatus{Phase: catalogv1.AlbumPhaseWanted}),
		album("kid-a", "radiohead", "Kid A", "Album", date(2000, time.October, 2), catalogv1.AlbumStatus{Phase: catalogv1.AlbumPhaseWanted}),
	).WithIndex(&catalogv1.Album{}, ui.AlbumArtistRefField, ui.IndexAlbumByArtistRef).Build()
	items := []projection.LibraryItem{
		{
			Ref: types.NamespacedName{Namespace: "default", Name: "led-zeppelin"}, Kind: commonv1.MediaKindArtist,
			Tab: projection.TabMusic, Title: "Led Zeppelin", QualityProfileRef: "music-lossless", Monitored: true,
		},
		{
			Ref: types.NamespacedName{Namespace: "default", Name: "pink-floyd"}, Kind: commonv1.MediaKindArtist,
			Tab: projection.TabMusic, Title: "Pink Floyd", QualityProfileRef: "music-lossless", Monitored: true, HasFile: true,
			Poster: projection.ArtURL(commonv1.MediaKindArtist, "pf-uid", catalogv1.ImageTypePoster, "pf-poster"),
			ArtKey: projection.ArtKey(commonv1.MediaKindArtist, "pf-uid", catalogv1.ImageTypePoster),
		},
	}
	srv := ui.NewServer(t.Context(), ui.Options{
		Reader:  c,
		Actions: actions.New(c),
		Library: func(context.Context) []projection.LibraryItem { return items },
	})
	return srv, c
}

// musicRelease is one result of an album's search, in Lidarr's quality
// vocabulary ("FLAC 24bit", "MP3-320").
func musicRelease(guid string, rank int32, title, quality string, rejections ...commonv1.Rejection) commonv1.ReleaseDecision {
	published := metav1.NewTime(time.Now().Add(-50 * time.Hour))
	return commonv1.ReleaseDecision{
		ReleaseInfo: commonv1.ReleaseInfo{
			GUID: guid, Title: title, IndexerName: "nzbgeek", Protocol: commonv1.ProtocolUsenet,
			SizeBytes: 900 << 20, PublishedAt: &published, Quality: commonv1.Quality{Name: quality}, FormatScore: 5,
		},
		Approved: len(rejections) == 0, Rejections: rejections, Rank: rank,
	}
}

// The artist page is Lidarr's: the hero reads the artist's facts and the
// rollup's status, the action bar has no interactive search, and the
// albums are one table per type, Albums, EPs, Singles in that order, each
// row with the status cell, the two searches and a title that opens the
// album's page.
func TestArtistPageGroupsAlbumsByTypeWithStatusCellsAndSearches(t *testing.T) {
	srv, _ := musicFixture(t)
	rec := getPath(t, srv, "/library/default/artist/pink-floyd", false)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "<html")

	hero := requireTag(t, body, `data-artist-page="pink-floyd"`, `data-hero`, `data-kind="artist"`, `data-monitored="true"`, `data-profile="music-lossless"`)
	require.NotEmpty(t, hero)
	require.Contains(t, body, `data-backdrop src="/art/artist/pf-uid/fanart?v=pf-fanart"`, "the hero is on the artist's fanart")
	require.Contains(t, body, `data-artist-type="Group"`)
	require.Contains(t, body, `data-artist-country>United Kingdom<`, "the country code reads as its name")
	require.Contains(t, body, `data-artist-years>1965 – 2014<`, "years active from the life-span")
	require.Contains(t, body, `data-artist-summary>4 albums, 1 missing, 1 below cutoff<`, "the Status fact is the rollup's")
	require.Contains(t, body, `data-fact="sortname"`)
	require.Contains(t, body, "Floyd, Pink")
	require.Contains(t, body, "UK rock band")
	require.Contains(t, body, "Progressive Rock, Psychedelic Rock")
	require.Contains(t, body, "Pink Floyd were an English rock band.")
	requireTag(t, body, `data-provider-link="musicbrainz"`, `href="https://musicbrainz.org/artist/83d91898-7763-47d7-b03b-b92132375c47"`)
	requireTag(t, body, `data-provider-link="discogs"`, `href="https://www.discogs.com/artist/45467"`)
	requireTag(t, body, `data-provider-link="wikipedia"`, `href="https://en.wikipedia.org/wiki/Pink_Floyd"`)
	requireTag(t, body, `href="https://pinkfloyd.bandcamp.com/"`, `title="Bandcamp"`, `data-provider-link=""`)
	require.NotContains(t, body, "wikidata.org", "a Wikidata entity is no page a person reads")

	bar := section(t, body, `data-toolbar`)
	bar = bar[:strings.Index(bar, `data-hero`)]
	for _, action := range []string{"refresh-metadata", "search-now", "rename-dry-run", "rename-apply", "delete"} {
		require.Contains(t, bar, `data-action="`+action+`"`, action)
	}
	require.Contains(t, bar, `data-edit-dialog="pink-floyd"`)
	require.NotContains(t, bar, `interactive-search`, "an artist's interactive searches are its albums'")
	require.Regexp(t, `data-action="search-now"[^>]*title="Search Monitored Albums"`, bar)

	albums := section(t, body, `data-section="albums"`)
	require.Contains(t, albums, `data-albums="7"`)
	require.Less(t, indexOf(albums, `data-album-group="album"`), indexOf(albums, `data-album-group="ep"`), "Albums before EPs")
	require.Less(t, indexOf(albums, `data-album-group="ep"`), indexOf(albums, `data-album-group="single"`), "EPs before Singles")
	require.NotContains(t, albums, `data-album-group="broadcast"`, "an empty group is not drawn")
	require.NotContains(t, albums, `data-album-group="other"`)
	requireTag(t, albums, `data-album-group="album"`, `data-album-group-count="5"`)
	require.Contains(t, albums, `data-album-group-files="1 / 5"`)
	require.Equal(t, 3, strings.Count(albums, `data-album-table`), "one table per group")
	require.Contains(t, albums, `data-open`, "the groups are open as the page lands")

	for name, status := range map[string]string{
		"dsotm": "file", "wish": "missing", "animals-live": "downloading", "money-single": "pending",
		"early-ep": "unmonitored", "forthcoming": "unreleased", "undated": "missing",
	} {
		requireTag(t, albums, `data-album="`+name+`"`, `data-slot="table-row"`, `data-status="`+status+`"`)
	}
	require.NotContains(t, albums, `data-album="kid-a"`, "another artist's album is never listed")
	require.Less(t, indexOf(albums, `data-album="forthcoming"`), indexOf(albums, `data-album="animals-live"`), "newest first")
	require.Less(t, indexOf(albums, `data-album="animals-live"`), indexOf(albums, `data-album="wish"`))
	require.Less(t, indexOf(albums, `data-album="wish"`), indexOf(albums, `data-album="dsotm"`))
	require.Less(t, indexOf(albums, `data-album="dsotm"`), indexOf(albums, `data-album="undated"`), "undated last")

	require.Regexp(t, `class="[^"]*bg-amber-500/20[^"]*"[^>]*title="Tracks are missing: 2/3 on disk"[^>]*>FLAC · 2/3<`, albums,
		"an album with tracks missing is an amber quality badge with its track summary")
	requireTag(t, albums, `data-album="animals-live"`, `data-type="album"`)
	require.Contains(t, albums, `data-secondary-type="Live"`)
	requireTag(t, albums, `data-album-type="single"`)
	require.Regexp(t, `data-album-type="single"[^>]*>\s*Single\s*<`, albums)
	require.Contains(t, albums, `<time datetime="1973-03-01" title="1973-03-01">Mar 1 1973</time>`)
	requireTag(t, albums, `href="/library/default/album/wish"`, `preload="mouseover"`, `data-album-title`)
	require.Contains(t, albums, `hx-post="/library/default/album/wish/search" hx-target="#action-status"`)
	require.Contains(t, albums, `hx-post="/library/default/album/wish/search/interactive" hx-target="#album-modal"`)
	require.Contains(t, albums, `hx-post="/library/default/album/wish/monitor" hx-target="closest tr"`)
	require.Contains(t, albums, `name="return" value="/library/default/artist/pink-floyd"`)

	require.Equal(t, 1, strings.Count(body, `id="album-modal"`))
	require.Equal(t, 1, strings.Count(body, `id="action-status"`))
	require.Equal(t, "private, max-age=10", rec.Header().Get("Cache-Control"), "a card's link preloads the page")

	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/artist/nope", false).Code)
}

// The album page is the episode page's analogue: the hero on the artist's
// fanart with the cover, the artist link, release date, type, label,
// duration, track summary and MusicBrainz logos, the facts with the
// catalog's cutoff reason, the Tracks table with each track's file cells
// and status dot, the Files section in Lidarr's columns and the
// neighbouring albums.
func TestAlbumPageShowsTracksFilesAndFacts(t *testing.T) {
	srv, _ := musicFixture(t)
	rec := getPath(t, srv, "/library/default/album/dsotm", false)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "Pink Floyd - The Dark Side of the Moon")

	requireTag(t, body, `data-album-page="dsotm"`, `data-hero`, `data-kind="album"`, `data-monitored="true"`, `data-hasfile="true"`, `data-status="file"`, `data-profile="music-lossless"`)
	require.Contains(t, body, `data-backdrop src="/art/artist/pf-uid/fanart?v=pf-fanart"`, "the hero is on the artist's fanart")
	requireTag(t, body, `data-cover`, `src="/art/album/dsotm-uid/poster?v=dsotm-cover"`, `data-art="album/dsotm-uid/poster"`)
	requireTag(t, body, `data-artist-link`, `href="/library/default/artist/pink-floyd"`)
	require.NotContains(t, body, `data-artist-credit`, "a credit equal to the artist's name is not repeated")
	require.Contains(t, body, `data-album-type="album"`)
	require.Contains(t, body, `data-release-date>Released Mar 1 1973<`)
	require.Contains(t, body, `data-album-label>Harvest<`)
	require.Contains(t, body, `data-album-duration>8m<`, "the tracks' lengths summed")
	require.Contains(t, body, `data-track-summary>2 of 3 tracks<`)
	requireTag(t, body, `href="https://musicbrainz.org/release-group/rg-dsotm"`, `data-provider-link="musicbrainz"`, `title="MusicBrainz (release group)"`)
	requireTag(t, body, `href="https://musicbrainz.org/release/rel-1"`, `data-provider-link="musicbrainz"`, `title="MusicBrainz (release)"`)
	requireTag(t, body, `href="https://www.discogs.com/master/10362"`, `data-provider-link="discogs"`)

	require.Regexp(t, `data-fact="status"[\s\S]*?data-status="cutoff-unmet"[\s\S]*?Cutoff Unmet: tracks missing`, body)
	require.Regexp(t, `data-fact="quality"[\s\S]*?bg-amber-500/20[^>]*>FLAC<`, body, "the quality fact is amber below the cutoff")
	require.Regexp(t, `data-fact="profile"[\s\S]*?>music-lossless<`, body)
	require.Regexp(t, `data-fact="monitored"[\s\S]*?data-monitored-fact="true">Monitored<`, body)
	require.Regexp(t, `data-fact="path"[\s\S]*?The Dark Side of the Moon \(1973\)`, body)
	require.Regexp(t, `data-fact="size"[\s\S]*?60.00 MiB`, body, "both files")
	require.Contains(t, body, "The eighth studio album by Pink Floyd.")

	tracks := section(t, body, `data-section="tracks"`)
	require.Contains(t, tracks, `data-tracks="3"`)
	require.Less(t, indexOf(tracks, `data-track="1"`), indexOf(tracks, `data-track="2"`), "in track order, whatever the status order")
	require.Less(t, indexOf(tracks, `data-track="2"`), indexOf(tracks, `data-track="3"`))
	one := requireTag(t, tracks, `data-track="1"`, `data-track-status="attributed"`)
	require.NotEmpty(t, one)
	requireTag(t, tracks, `data-track="3"`, `data-track-status="missing"`)
	require.Contains(t, tracks, `data-track-title>Speak to Me<`)
	require.Contains(t, tracks, `data-explicit`)
	require.Contains(t, tracks, ">1:30<", "a track's duration")
	require.Contains(t, tracks, ">3:30<")
	require.Regexp(t, `data-cell="codec"[^>]*>FLAC<`, tracks)
	require.Regexp(t, `data-cell="bitrate"[^>]*>1411 kbps<`, tracks)
	require.Regexp(t, `data-cell="sampleRate"[^>]*>44.1 kHz / 16-bit<`, tracks)
	require.Equal(t, 2, strings.Count(tracks, `data-cell="codec"`), "the missing track has no file cells")
	require.Contains(t, tracks, `title="Missing from disk"`)
	require.Contains(t, tracks, `title="On disk: 01 - Speak to Me.flac"`)

	files := section(t, body, `data-section="files"`)
	require.Contains(t, files, `data-file="dsotm-01"`)
	require.Contains(t, files, `data-file="dsotm-02"`)
	require.Contains(t, files, ">Sample Rate<", "Lidarr's columns")
	require.NotContains(t, files, ">Video Codec<")
	require.Contains(t, files, "02 - Breathe.flac")
	require.NotContains(t, files, "The Dark Side of the Moon (1973)/02 - Breathe.flac", "the path relative to the album's folder")

	bar := section(t, body, `data-toolbar`)
	bar = bar[:strings.Index(bar, `data-hero`)]
	requireTag(t, bar, `id="refresh-dsotm"`, `action="/library/default/album/dsotm/refresh"`)
	require.Regexp(t, `data-action="refresh-metadata"[^>]*title="Refresh Album"`, bar)
	requireTag(t, bar, `id="search-dsotm"`, `action="/library/default/album/dsotm/search"`)
	requireTag(t, bar, `id="interactive-search-dsotm"`, `hx-post="/library/default/album/dsotm/search/interactive"`, `hx-target="#album-modal"`)
	require.Contains(t, bar, `data-action="album-interactive-search"`)
	// The rename is the artist's folder's: an album's files live under it.
	requireTag(t, bar, `id="rename-dry-run-dsotm"`, `action="/library/default/artist/pink-floyd/rename"`)
	// Edit is the album's own (design §9.2): the dialog over the album's
	// card, its form posting to the album's edit route and returning to
	// its page, its kind block fetched from the album's edit/fields route
	// as the dialog opens, and no profile select beside Monitored (the
	// profile is in the block, an override of the artist's).
	requireTag(t, bar, `data-edit-dialog="dsotm"`)
	require.Contains(t, bar, "Edit - The Dark Side of the Moon")
	requireTag(t, bar, `data-edit-form`, `action="/library/default/album/dsotm/edit"`)
	require.Contains(t, bar, `name="return" value="/library/default/album/dsotm"`)
	requireTag(t, bar, `data-edit-fields="album"`, `hx-get="/library/default/album/dsotm/edit/fields"`, `hx-trigger="intersect once"`)
	require.NotContains(t, bar, `name="qualityProfile"`, "an album's profile is in its block")
	require.NotContains(t, bar, `data-edit-dialog="pink-floyd"`, "the artist's Edit stays on the artist's page")
	// Delete is the album's own too (design §9.2; catalogv1.DeletableKinds
	// since 2026-11-10): the dialog over the album's card names the album's
	// folder and its two files, posts to the album's delete route, and
	// offers no import list exclusion: the album's exclusion kind exists,
	// but catalogv1.ExclusionKindFor withholds it until the Artist's fan-out
	// honours one, since a deleted album of a listed release group comes
	// back at the artist's next fan-out whatever the exclusion says.
	requireTag(t, bar, `data-action="delete"`)
	require.Contains(t, bar, "Delete The Dark Side of the Moon")
	requireTag(t, bar, `action="/library/default/album/dsotm/delete"`)
	require.Contains(t, section(t, bar, `data-delete-folder`), "/data/music/Pink Floyd/The Dark Side of the Moon (1973)")
	require.Regexp(t, `data-delete-files>[^<]*2 file\(s\)`, bar)
	requireTag(t, bar, `name="files"`, `value="true"`)
	require.NotContains(t, bar, `name="exclude"`, "an album's exclusion is withheld until the Artist's fan-out honours it")
	require.NotContains(t, bar, `data-action="delete-cancel"`, "no delete is pending")

	requireTag(t, body, `data-nav="next"`, `href="/library/default/album/money-single"`)
	require.NotContains(t, body, `data-nav="prev"`, "the earliest album has no previous")
	require.Equal(t, 1, strings.Count(body, `id="album-modal"`))
	require.Equal(t, 1, strings.Count(body, `id="action-status"`))

	later := getPath(t, srv, "/library/default/album/wish", false).Body.String()
	requireTag(t, later, `data-nav="prev"`, `href="/library/default/album/money-single"`)
	requireTag(t, later, `data-nav="next"`, `href="/library/default/album/animals-live"`)
	require.Regexp(t, `data-fact="status"[\s\S]*?data-status="missing"[\s\S]*?Missing`, later)
	require.Contains(t, later, `data-tracks="0"`)

	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/album/nope", false).Code)
	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/album/kid-a", false).Code,
		"an album whose artist the library does not hold")
}

// The album page's Edit dialog saves the album (design §9.2): its block
// comes from the album's edit/fields route, its form posts to the album's
// edit route with the block's fields, and the Album is patched -- a
// profile override, monitoring off, the release pinned -- after which the
// page reads the override as the album's profile where it read the
// artist's, and the dialog's switch is off.
func TestAlbumPageEditDialogSavesTheAlbum(t *testing.T) {
	srv, c := musicFixture(t)
	fields := getPath(t, srv, "/library/default/album/dsotm/edit/fields", true)
	require.Equal(t, http.StatusOK, fields.Code)
	requireTag(t, fields.Body.String(), `name="fields"`, `value="album"`)

	rec := postForm(t, srv, "/library/default/album/dsotm/edit", url.Values{
		"return": {"/library/default/album/dsotm"}, "fields": {"album"}, "qualityProfile": {"music-mp3"},
		"monitored": {"false"}, "anyReleaseOk": {"false"}, "releaseID": {"rel-1"},
	}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.Equal(t, "/library/default/album/dsotm", rec.Header().Get("Location"), "back on the album's page")
	var got catalogv1.Album
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "dsotm"}, &got))
	require.False(t, *got.Spec.Monitored)
	require.Equal(t, "music-mp3", *got.Spec.QualityProfileRef)
	require.False(t, *got.Spec.AnyReleaseOk)
	require.Equal(t, "rel-1", *got.Spec.ReleaseID)

	page := getPath(t, srv, "/library/default/album/dsotm", false).Body.String()
	requireTag(t, page, `data-album-page="dsotm"`, `data-profile="music-mp3"`, `data-monitored="false"`)
	require.Regexp(t, `data-fact="profile"[\s\S]*?>music-mp3<`, page, "the override is the album's profile")
	requireTag(t, page, `id="edit-monitored-dsotm"`, `name="monitored"`)
	require.NotRegexp(t, `id="edit-monitored-dsotm"[^>]*checked`, page, "the dialog's switch reads the live Album")
	require.Regexp(t, `data-fact="profile"[\s\S]*?>music-lossless<`, getPath(t, srv, "/library/default/album/wish", false).Body.String(),
		"an album without an override reads its artist's profile")
}

// The title opens the album's details modal for htmx and the album's page
// for anyone else: the heading, the facts line, the overview, the genres
// and the files in Lidarr's columns, Details open; ?tab=search opens the
// Search tab, whose Interactive Search swaps in the album panel.
func TestAlbumDetailsServesTheModalToHTMXAndAPageOtherwise(t *testing.T) {
	srv, _ := musicFixture(t)
	rec := getPath(t, srv, "/library/default/album/dsotm/details", true)
	require.Equal(t, http.StatusOK, rec.Code)
	modal := rec.Body.String()
	require.NotContains(t, modal, "<html")
	require.Contains(t, modal, `data-album-modal="dsotm"`)
	require.Contains(t, modal, "Pink Floyd - The Dark Side of the Moon")
	require.Contains(t, modal, "Released Mar 1 1973 · Album · 2/3 tracks · FLAC · 2/3")
	require.Contains(t, modal, "The eighth studio album by Pink Floyd.")
	require.Contains(t, modal, ">Progressive Rock<")
	requireTag(t, modal, `data-album-files="2"`)
	require.Contains(t, modal, `data-file="dsotm-01"`)
	require.Contains(t, modal, ">Bitrate<")
	require.Contains(t, modal, "01 - Speak to Me.flac")
	requireTag(t, modal, `data-tab="details"`, `aria-selected="true"`)
	require.Contains(t, modal, `data-album-search="dsotm"`)
	require.Contains(t, modal, "Automatic Search")
	require.Contains(t, modal, "Interactive Search")

	searchTab := getPath(t, srv, "/library/default/album/wish/details?tab=search", true).Body.String()
	requireTag(t, searchTab, `data-tab="search"`, `aria-selected="true"`)
	require.Contains(t, searchTab, `data-album-files="0"`, "no file on disk")
	require.Contains(t, searchTab, `hx-select="#album-search-body"`)
	require.Contains(t, searchTab, `hx-post="/library/default/album/wish/search/interactive"`)

	page := getPath(t, srv, "/library/default/album/dsotm/details", false)
	require.Equal(t, http.StatusOK, page.Code)
	require.Contains(t, page.Body.String(), "<html")
	require.Contains(t, page.Body.String(), `data-album-page="dsotm"`)

	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/album/nope/details", true).Code)
	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/album/kid-a/details", true).Code)
}

// The interactive search creates a Search for the album that grabs nothing
// and answers the album's search modal over the shared panel, which polls
// the shared /searches route (so one panel serves every kind) and draws
// Lidarr's columns for an album's Search: Quality, Size, Age, Indexer,
// Peers and Score, grabbed through the shared grab route, which answers the
// same columns. The album's own panel and grab routes are gone. A form post
// lands on the panel's page; an unknown album creates nothing.
func TestAlbumInteractiveSearchOpensLidarrsColumnsThroughTheSharedPanel(t *testing.T) {
	srv, c := musicFixture(t)
	rec := postForm(t, srv, "/library/default/album/wish/search/interactive", url.Values{}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	var list catalogv1.SearchList
	require.NoError(t, c.List(t.Context(), &list, client.InNamespace("default")))
	require.Len(t, list.Items, 3)
	var created catalogv1.Search
	for _, s := range list.Items {
		if strings.HasPrefix(s.Name, "wish-") && s.Name != "wish-answered" {
			created = s
		}
	}
	require.NotEmpty(t, created.Name)
	require.Equal(t, &commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "wish"}, created.Spec.MediaRef)
	require.False(t, created.Spec.GrabBest, "an interactive search grabs only what a person picks")

	require.Contains(t, body, `data-album-search-modal="wish"`)
	require.Contains(t, body, "Interactive Search - Pink Floyd - Wish You Were Here")
	requireTag(t, body, `id="album-search-body"`, `data-album-search="wish"`)
	requireTag(t, body, `data-search-panel="`+created.Name+`"`,
		`hx-get="/searches/default/`+created.Name+`"`, `hx-trigger="load delay:2s"`, `hx-swap="outerHTML"`)
	require.Contains(t, body, "Searching indexers")

	panel := getPath(t, srv, "/searches/default/wish-answered", true)
	require.Equal(t, http.StatusOK, panel.Code)
	results := panel.Body.String()
	require.NotContains(t, results, "<html")
	requireTag(t, results, `data-search-panel="wish-answered"`, `data-search-phase="Completed"`)
	require.NotContains(t, results, `hx-trigger="load delay:2s"`, "an answered search stops polling")
	require.Contains(t, results, "2 results from 1 indexers")
	table := requireTag(t, results, `data-release-table`, `data-release-columns="music"`)
	require.NotEmpty(t, table)
	for _, head := range []string{">Quality<", ">Size<", ">Age<", ">Indexer<", ">Peers<", ">Score<"} {
		require.Contains(t, results, head, head)
	}
	require.NotContains(t, results, ">Languages<", "no language column for music")
	require.Less(t, indexOf(results, `data-release="guid-flac"`), indexOf(results, `data-release="guid-mp3"`), "ranked order")
	requireTag(t, results, `data-release="guid-flac"`, `data-approved="true"`)
	require.Regexp(t, `data-cell="quality"[^>]*>[\s\S]*?>FLAC 24bit<`, results)
	require.Regexp(t, `data-cell="quality"[^>]*>[\s\S]*?>MP3-320<`, results)
	require.Contains(t, results, "900.00 MiB")
	require.Contains(t, results, "2 days")
	require.Contains(t, results, `data-rejections="1"`)
	require.Contains(t, results, `hx-post="/searches/default/wish-answered/grab"`, "the download button grabs through the shared route")
	require.Contains(t, results, `hx-confirm="This release was rejected:`, "a permanent rejection asks first")

	grab := postForm(t, srv, "/searches/default/wish-answered/grab", url.Values{"guid": {"guid-flac"}}, true)
	require.Equal(t, http.StatusOK, grab.Code)
	grabbed := grab.Body.String()
	requireTag(t, grabbed, `data-release="guid-flac"`, `data-grab="requested"`)
	require.Contains(t, grabbed, `data-grab-state="requested"`)
	require.Contains(t, grabbed, `data-release-columns="music"`, "the grab keeps the columns")
	// A requested grab polls until the owner answers, through the shared
	// route, which keeps the columns too.
	requireTag(t, grabbed, `data-search-panel="wish-answered"`, `hx-get="/searches/default/wish-answered"`, `hx-trigger="load delay:2s"`)
	var got catalogv1.Search
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "wish-answered"}, &got))
	require.Equal(t, []string{"guid-flac"}, got.Spec.Grab)

	bad := postForm(t, srv, "/searches/default/wish-answered/grab", url.Values{"guid": {"nope"}}, true)
	require.Equal(t, http.StatusOK, bad.Code)
	require.Contains(t, bad.Body.String(), `data-action-error="invalid"`)
	require.Contains(t, bad.Body.String(), `data-release-columns="music"`, "a failed grab is rendered in the album's columns")

	// The album's own panel and grab routes are gone (C1, 2026-11-10).
	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/album/wish/search/panel/wish-answered", true).Code)
	require.Equal(t, http.StatusNotFound, postForm(t, srv, "/library/default/album/wish/search/panel/wish-answered/grab", url.Values{"guid": {"guid-flac"}}, true).Code)

	rec = postForm(t, srv, "/library/default/album/wish/search/interactive", url.Values{}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.True(t, strings.HasPrefix(rec.Header().Get("Location"), "/searches/default/wish-"), rec.Header().Get("Location"))

	rec = postForm(t, srv, "/library/default/album/nope/search/interactive", url.Values{}, true)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.NoError(t, c.List(t.Context(), &list, client.InNamespace("default")))
	require.Len(t, list.Items, 4, "only the form post's Search; none for an unknown album")
}

// While import carries an album's delete out the page hides the Delete
// button and the status fact reads Deleting with a Cancel; a refused one
// reads why and offers Retry as it was asked (files, no exclusion) beside
// the Cancel -- the album's card carries its delete annotations as a
// projection card does (ui.albumItem).
// An album's Delete dialog names the album's own folder alone (design
// §9.2): an album whose files lie straight in its artist's folder -- its
// status.path the artist's, or a preset folder not on disk beside files
// that share the artist's -- has none of its own, import removes its files
// alone and leaves the artist's folder standing, so the dialog names no
// folder, counts the files and says so, where it named the artist's folder
// under "deletes the folder".
func TestAlbumPageDeleteNamesNoFolderForAnAlbumFlatInItsArtistsFolder(t *testing.T) {
	srv, c := musicFixture(t)
	setPaths := func(albumPath string) {
		t.Helper()
		var a catalogv1.Album
		require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "dsotm"}, &a))
		a.Status.Path = albumPath
		require.NoError(t, c.Update(t.Context(), &a))
		for _, name := range []string{"dsotm-01", "dsotm-02"} {
			var f catalogv1.MediaFile
			require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: name}, &f))
			f.Spec.Path = "/data/music/Pink Floyd/" + filepath.Base(f.Spec.Path)
			require.NoError(t, c.Update(t.Context(), &f))
		}
	}
	for _, albumPath := range []string{"/data/music/Pink Floyd", "/data/music/Pink Floyd/Not Here (1973)"} {
		setPaths(albumPath)
		body := getPath(t, srv, "/library/default/album/dsotm", false).Body.String()
		bar := section(t, body, `data-toolbar`)
		bar = bar[:strings.Index(bar, `data-hero`)]
		requireTag(t, bar, `data-action="delete"`)
		require.NotContains(t, bar, `data-delete-folder`, albumPath)
		require.Regexp(t, `data-delete-files>[^<]*2 file\(s\)`, bar)
		require.Contains(t, bar, "This permanently deletes the files and cannot be undone.")
		require.NotContains(t, bar, "deletes the folder")
		require.Regexp(t, `data-fact="path"[\s\S]*?`+regexp.QuoteMeta(albumPath), body, "the Path fact still reads the album's path")
	}

	d := views.Detail{Path: "/data/music/Pink Floyd/DSOTM", SharedFolder: "/data/music/Pink Floyd"}
	require.Equal(t, "/data/music/Pink Floyd/DSOTM", views.DeleteFolder(d), "a folder of the album's own")
	d.Path = "/data/music/Pink Floyd"
	require.Empty(t, views.DeleteFolder(d), "the artist's folder itself")
	d.Path = "/data/music/Pink Floyd/DSOTM"
	d.Files = []views.FileRow{{Path: "/data/music/Pink Floyd/01.flac"}}
	require.Empty(t, views.DeleteFolder(d), "files straight in the artist's folder, whatever the preset says")
	d.SharedFolder = ""
	require.Equal(t, "/data/music/Pink Floyd", views.DeleteFolder(d), "a kind with a folder of its own names the files' folder, as a scanned movie does")
}

func TestAlbumPageShowsAPendingDeleteAndHidesTheButton(t *testing.T) {
	srv, c := musicFixture(t)
	var a catalogv1.Album
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "dsotm"}, &a))
	a.Annotations = map[string]string{catalogv1.AnnotationDelete: catalogv1.DeleteFiles}
	require.NoError(t, c.Update(t.Context(), &a))

	body := getPath(t, srv, "/library/default/album/dsotm", false).Body.String()
	require.NotContains(t, body, `data-action="delete"`, "no second request while one is pending")
	status := section(t, body, `data-fact="status"`)
	require.Contains(t, status, "Deleting")
	requireTag(t, status, `data-action="delete-cancel"`)
	requireTag(t, status, `action="/library/default/album/dsotm/delete/cancel"`)
	require.NotContains(t, status, `data-action="delete-retry"`)

	a.Annotations[catalogv1.AnnotationDeleteError] = "folder also holds media file wywh-01"
	require.NoError(t, c.Update(t.Context(), &a))
	body = getPath(t, srv, "/library/default/album/dsotm", false).Body.String()
	status = section(t, body, `data-fact="status"`)
	require.Contains(t, status, "Delete failed: folder also holds media file wywh-01")
	retry := requireTag(t, status, `data-action="delete-retry"`)
	require.NotEmpty(t, retry)
	requireTag(t, status, `action="/library/default/album/dsotm/delete"`)
	require.Contains(t, status, `name="files" value="true"`)
	require.NotContains(t, status, `name="exclude"`)
	requireTag(t, status, `data-action="delete-cancel"`)
}
