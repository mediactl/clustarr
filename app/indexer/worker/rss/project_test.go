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

package rss_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/indexer/worker/rss"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/newznab"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/torznab"
)

func TestProjectReleaseFillsTheIndexerSourcedFields(t *testing.T) {
	seeders, leechers := int32(42), int32(7)
	in := torznab.Release{
		Title:      "The Matrix 1999 1080p BluRay x264-GROUP",
		GUID:       "https://idx.example/details/9001",
		Link:       "https://idx.example/download/9001.torrent",
		CommentURL: "https://idx.example/details/9001",
		PubDate:    time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
		Size:       8_589_934_592,
		Categories: []newznab.CategoryID{2040, 2000},
		Seeders:    &seeders,
		Leechers:   &leechers,
		InfoHash:   "0123456789abcdef0123456789abcdef01234567",
		MagnetURL:  "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
		IDs:        map[string]string{"imdb": "tt0133093"},
	}

	got := rss.ProjectRelease(in, "my-indexer", string(commonv1.ProtocolTorrent))

	require.Equal(t, "https://idx.example/details/9001", got.Info.GUID)
	require.Equal(t, "my-indexer", got.Info.IndexerRef)
	require.Equal(t, "The Matrix 1999 1080p BluRay x264-GROUP", got.Info.Title,
		"Info.Title is the RAW title as published, never a cleaned one")
	require.Equal(t, commonv1.ProtocolTorrent, got.Info.Protocol)
	require.Equal(t, int64(8_589_934_592), got.Info.SizeBytes)
	require.Equal(t, "https://idx.example/download/9001.torrent", got.Info.DownloadURL)
	require.Equal(t, "https://idx.example/details/9001", got.Info.InfoURL)
	require.Equal(t, []int32{2040, 2000}, got.Info.Categories)
	require.Equal(t, &seeders, got.Info.Seeders)
	require.Equal(t, &leechers, got.Info.Leechers)
	require.Equal(t, "tt0133093", got.Info.IDs["imdb"], "the tt prefix is canonical on the way in")
	require.Equal(t, "GROUP", got.Info.ReleaseGroup, "filled by ApplyTo, not by hand")
}

// TestProjectReleaseDoesNotAliasTheWireIDs proves the projection copies the
// indexer's IDs map rather than borrowing it. ApplyTo WRITES parsed ids into
// Info.IDs, so an aliased map would have the parser mutating the caller's
// torznab.Release -- and the caller is the poll loop, which keeps every
// fetched row alive for the index write that follows.
func TestProjectReleaseDoesNotAliasTheWireIDs(t *testing.T) {
	in := torznab.Release{
		Title: "The.Matrix.1999.1080p.BluRay.x264-GROUP[tmdbid-603]",
		GUID:  "g",
		IDs:   map[string]string{"imdb": "tt0133093"},
	}
	got := rss.ProjectRelease(in, "idx", "torrent")

	require.Equal(t, map[string]string{"imdb": "tt0133093"}, in.IDs,
		"ProjectRelease must not write parsed ids back into its argument")
	require.Equal(t, "603", got.Info.IDs["tmdb"], "the parsed id lands on the projection")
}

func TestProjectReleasePublishedAtIsNeverBackfilled(t *testing.T) {
	pub := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	usenet := time.Date(2026, 8, 30, 3, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		in   torznab.Release
		want *metav1.Time
	}{
		{"pubdate wins", torznab.Release{PubDate: pub}, ptr.To(metav1.NewTime(pub))},
		{"usenetdate when pubdate is absent", torznab.Release{UsenetDate: &usenet}, ptr.To(metav1.NewTime(usenet))},
		{"neither reported stays nil", torznab.Release{}, nil},
		{"an explicitly zero usenetdate stays nil", torznab.Release{UsenetDate: &time.Time{}}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rss.ProjectRelease(tt.in, "idx", "usenet")
			require.Equal(t, tt.want, got.Info.PublishedAt)
		})
	}
}

func TestProjectReleaseNilPublishedAtSurvivesJSONRoundTrip(t *testing.T) {
	// A nil that becomes a zero metav1.Time somewhere in encode/decode is the
	// same corruption by another route, so assert on the far side of the wire.
	in := rss.ProjectRelease(torznab.Release{Title: "Some.Release.2026", GUID: "g"}, "idx", "usenet")
	require.Nil(t, in.Info.PublishedAt)

	name, data, err := schema.Encode(in)
	require.NoError(t, err)
	var out schema.Release
	require.NoError(t, schema.Decode(name, data, &out))
	require.Nil(t, out.Info.PublishedAt, "nil publishedAt must round-trip as nil, not as the zero time")
	require.NotContains(t, string(data), "publishedAt", "omitempty must drop the key entirely")
}

func TestProjectReleaseParsedTitleIsRawAndKindAgrees(t *testing.T) {
	got := rss.ProjectRelease(torznab.Release{
		Title: "The.Matrix.1999.1080p.BluRay.x264-GROUP",
		GUID:  "g",
	}, "idx", "torrent")

	parsed, err := release.Parse("The.Matrix.1999.1080p.BluRay.x264-GROUP", release.Options{})
	require.NoError(t, err)
	require.Equal(t, parsed.Title, got.ParsedTitle,
		"ParsedTitle is the parser's raw Title; the matcher applies CleanTitle itself")
	require.Equal(t, int32(1999), got.Year)
	require.Equal(t, commonv1.MediaKindMovie, got.Kind)

	// The matcher's own key must be derivable from what we sent.
	require.Equal(t,
		release.CleanTitle(parsed.Title)+"|1999",
		release.CleanTitle(got.ParsedTitle)+"|"+strconv.Itoa(int(got.Year)))
}

func TestProjectReleaseSeriesFields(t *testing.T) {
	got := rss.ProjectRelease(torznab.Release{
		Title: "Some.Show.S02E05.1080p.WEB-DL.x265-GRP", GUID: "g",
	}, "idx", "torrent")
	require.Equal(t, commonv1.MediaKindEpisode, got.Kind)
	require.Equal(t, []int32{2}, got.Seasons)
	require.Equal(t, []int32{5}, got.Episodes)
	require.False(t, got.FullSeason)
	require.False(t, got.MultiSeason)
}

// TestProjectReleaseKindMatchesTheParseThatActuallyRan states the invariant
// ProjectRelease's single classification exists to hold: the Kind on the wire
// is the one that steered the parse whose fields we shipped.
//
// It is NOT a regression test for the classify-once change, and saying so is
// the point: reverting to a second, separate ClassifyKind call leaves every
// assertion here green, because Parse strips ids before classifying and the
// two answers agree for every title shape that reaches a feed. Where they do
// differ, pinning is the better of the two --
// TestProjectReleasePinningTheKindIsNeverWorseThanAutoDetecting measures it.
func TestProjectReleaseKindMatchesTheParseThatActuallyRan(t *testing.T) {
	const title = "Some.Show.S02E05.1080p.WEB-DL.x265-GRP[tvdbid-121361]"
	got := rss.ProjectRelease(torznab.Release{Title: title, GUID: "g"}, "idx", "torrent")

	parsed, err := release.Parse(title, release.Options{Kind: got.Kind})
	require.NoError(t, err)
	require.Equal(t, parsed.Title, got.ParsedTitle,
		"the wire Kind must be the one that steered the parse we shipped")
	require.Equal(t, widen32(parsed.Seasons), got.Seasons)
	require.Equal(t, widen32(parsed.Episodes), got.Episodes)
}

func widen32(in []int) []int32 {
	if len(in) == 0 {
		return nil
	}
	out := make([]int32, len(in))
	for i, v := range in {
		out[i] = int32(v)
	}
	return out
}

func TestProjectReleaseIndexerFlagsStayInsideTheEnum(t *testing.T) {
	zero, half := 0.0, 0.5
	tests := []struct {
		name string
		in   torznab.Release
		want []string
	}{
		{"dvf 0 is freeleech", torznab.Release{DownloadVolumeFactor: &zero}, []string{"freeleech"}},
		{"dvf 0.5 is halfleech", torznab.Release{DownloadVolumeFactor: &half}, []string{"halfleech"}},
		{"dvf 1 is neither", torznab.Release{DownloadVolumeFactor: ptr.To(1.0)}, nil},
		// Sonarr's Freeleech25/Freeleech75 have no CRD member. They are
		// dropped, not rounded onto halfleech, which they are not.
		{"dvf 0.75 has no enum member", torznab.Release{DownloadVolumeFactor: ptr.To(0.75)}, nil},
		{"dvf 0.25 has no enum member", torznab.Release{DownloadVolumeFactor: ptr.To(0.25)}, nil},
		{"uvf 2 is doubleupload", torznab.Release{UploadVolumeFactor: ptr.To(2.0)}, []string{"doubleupload"}},
		{"uvf 1 is nothing", torznab.Release{UploadVolumeFactor: ptr.To(1.0)}, nil},
		{"freeleech and doubleupload together", torznab.Release{
			DownloadVolumeFactor: &zero, UploadVolumeFactor: ptr.To(2.0),
		}, []string{"freeleech", "doubleupload"}},
		{"a factor flag and the same tag do not double up", torznab.Release{
			DownloadVolumeFactor: &zero, Attrs: map[string][]string{"tag": {"freeleech"}},
		}, []string{"freeleech"}},
		{"tag attrs pass through when known", torznab.Release{
			Attrs: map[string][]string{"tag": {"internal", "scene"}},
		}, []string{"internal", "scene"}},
		{"unknown tags are dropped, not forwarded", torznab.Release{
			Attrs: map[string][]string{"tag": {"internal", "PersonalRelease", "trumpable"}},
		}, []string{"internal"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rss.ProjectRelease(tt.in, "idx", "torrent")
			require.Equal(t, tt.want, got.Info.IndexerFlags)
		})
	}
}

// TestProjectReleaseIndexerFlagsAreAllEnumMembers holds the allow-list to the
// CRD's closed enum from the other direction: every value the mapping can
// emit must be one the apiserver accepts. Otherwise the rejection surfaces at
// grab time, in grabarr, when Download.spec.release is persisted -- far from
// here and long after the release looked fine.
func TestProjectReleaseIndexerFlagsAreAllEnumMembers(t *testing.T) {
	allowed := map[string]bool{
		commonv1.IndexerFlagFreeleech: true, commonv1.IndexerFlagHalfleech: true,
		commonv1.IndexerFlagNeutralleech: true, commonv1.IndexerFlagDoubleUpload: true,
		commonv1.IndexerFlagInternal: true, commonv1.IndexerFlagExclusive: true,
		commonv1.IndexerFlagScene: true,
	}
	got := rss.ProjectRelease(torznab.Release{
		DownloadVolumeFactor: ptr.To(0.0),
		Attrs: map[string][]string{"tag": {
			"FreeLeech", "halfleech", "NEUTRALLEECH", "doubleupload",
			"internal", "exclusive", "scene", "nuked", "",
		}},
	}, "idx", "torrent")

	require.NotEmpty(t, got.Info.IndexerFlags)
	for _, f := range got.Info.IndexerFlags {
		require.True(t, allowed[f], "flag %q is outside ReleaseInfo.IndexerFlags' enum", f)
	}
	require.Len(t, got.Info.IndexerFlags, len(allowed), "every enum member is reachable")
}

func TestProjectReleaseLeavesScoringToTheConsumer(t *testing.T) {
	got := rss.ProjectRelease(torznab.Release{Title: "X.2026.1080p-G", GUID: "g"}, "idx", "torrent")
	require.Zero(t, got.Info.FormatScore, "pkg/decision.Evaluate writes this on the consumer side")
	require.Empty(t, got.Info.MatchedFormats)
	require.Zero(t, got.FetchedAt, "the publisher stamps FetchedAt, so exactly one place owns it")
}

// TestProjectReleaseKeepsAnUnparsableTitle proves a title the parser refuses
// is still published. It can match on tmdb/tvdb ids alone, and dropping it
// here would hide it from the matcher entirely.
func TestProjectReleaseKeepsAnUnparsableTitle(t *testing.T) {
	got := rss.ProjectRelease(torznab.Release{
		Title: "   ", GUID: "g", Size: 42,
		IDs: map[string]string{commonv1.IDKeyTMDB: "603"},
	}, "idx", "torrent")

	require.Equal(t, "g", got.Info.GUID)
	require.Equal(t, int64(42), got.Info.SizeBytes)
	require.Equal(t, "603", got.Info.IDs[commonv1.IDKeyTMDB])
	require.Empty(t, got.ParsedTitle)
}

// TestProjectReleaseClassifiesRealisticIDShapes pins the shapes that actually
// reach a Torznab feed.
//
// rel.Kind is the matcher's FIRST dispatch and anything that is not the right
// kind matches nothing at all, silently -- and the classification runs on the
// raw title, id token included, while Parse runs on the id-stripped one. A
// trailing "[tmdbid-603]" or "[imdbid-tt0133093]" must not be able to turn a
// movie into something else on its way past the classifier.
func TestProjectReleaseClassifiesRealisticIDShapes(t *testing.T) {
	tests := []struct {
		title string
		kind  commonv1.MediaKind
	}{
		{"The.Matrix.1999.1080p.BluRay.x264-GRP[tmdbid-603]", commonv1.MediaKindMovie},
		{"The.Matrix.1999.1080p.BluRay.x264-GRP [imdbid-tt0133093]", commonv1.MediaKindMovie},
		{"The.Matrix.1999.1080p.BluRay.x264-GRP", commonv1.MediaKindMovie},
		{"Some.Show.S02E05.1080p.WEB-DL.x265-GRP[tvdbid-121361]", commonv1.MediaKindEpisode},
		{"Some.Show.S02E05.1080p-GRP", commonv1.MediaKindEpisode},
		{"[SubsPlease] Show - 12 (1080p) [ABCD1234].mkv", commonv1.MediaKindEpisode},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			got := rss.ProjectRelease(torznab.Release{Title: tt.title, GUID: "g"}, "idx", "torrent")
			require.Equal(t, tt.kind, got.Kind)
			require.NotEmpty(t, got.ParsedTitle, "a kind the parser refuses ships no parsed fields at all")
		})
	}
}

// TestProjectReleasePinningTheKindIsNeverWorseThanAutoDetecting measures the
// claim the implementation rests on, rather than asserting it.
//
// Parse strips ids and then classifies, while ClassifyKind sees the raw
// title, so the two can disagree -- and when they do, the question is which
// is better, not merely which is consistent. For a leading id token, pinning
// is the only one that parses at all; for the brace form, both fail
// identically, which is a pkg/release defect (see the KNOWN LIMITATION on
// ProjectRelease) and not something pinning caused.
func TestProjectReleasePinningTheKindIsNeverWorseThanAutoDetecting(t *testing.T) {
	titles := []string{
		"The.Matrix.1999.1080p.BluRay.x264-GROUP",
		"The.Matrix.1999.1080p.BluRay.x264-GROUP[tmdbid-603]",
		"Some.Show.S02E05.1080p.WEB-DL.x265-GRP[tvdbid-121361]",
		"[tmdbid-603] Show - 12 [1080p].mkv",
		"[SubsPlease] Show - 12 (1080p) [ABCD1234].mkv",
		"Some Show S01E01 {tvdbid-121361}",
	}
	for _, title := range titles {
		t.Run(title, func(t *testing.T) {
			_, autoErr := release.Parse(title, release.Options{})
			got := rss.ProjectRelease(torznab.Release{Title: title, GUID: "g"}, "idx", "torrent")

			if autoErr == nil {
				require.NotEmpty(t, got.ParsedTitle,
					"auto-detection parses this and pinning does not: pinning is worse here")
				require.NotEmpty(t, got.Kind)
			}
		})
	}

	// The specific shape where pinning is strictly better. If this ever
	// starts parsing under auto-detection too, the claim above has become
	// merely "not worse" and the comment should say so.
	const leading = "[tmdbid-603] Show - 12 [1080p].mkv"
	_, autoErr := release.Parse(leading, release.Options{})
	require.Error(t, autoErr, "premise changed: auto-detection now handles a leading id token")
	require.NotEmpty(t, rss.ProjectRelease(torznab.Release{Title: leading, GUID: "g"}, "idx", "torrent").ParsedTitle)
}

// ReleaseInfo.Categories carries MaxItems=50 and is indexer-supplied: one
// tracker listing more would get a whole Search.status (or a grab's
// Download) rejected, so projection keeps the first 50.
func TestProjectReleaseCapsCategoriesAtTheCRDsMaxItems(t *testing.T) {
	cats := make([]newznab.CategoryID, 75)
	for i := range cats {
		cats[i] = newznab.CategoryID(2000 + i)
	}
	got := rss.ProjectRelease(torznab.Release{Title: "Some Movie 2020 1080p WEB-DL", GUID: "g", Categories: cats}, "idx", "torrent")
	require.Len(t, got.Info.Categories, 50)
	require.Equal(t, int32(2000), got.Info.Categories[0])
}

// TestProjectReleaseCarriesNonVideoNames: the names the RSS matcher finds an
// album, a book, an audiobook or a comic issue by ride on the wire -- the
// indexer's own Newznab attr where it sent one, the parsed title otherwise
// -- and only for the kind the release was parsed as.
func TestProjectReleaseCarriesNonVideoNames(t *testing.T) {
	for _, c := range []struct {
		name                         string
		in                           torznab.Release
		kind                         commonv1.MediaKind
		parsedTitle                  string
		artist, album, author, issue string
	}{
		{
			name: "an album, named by its title",
			in:   torznab.Release{Title: "Radiohead - OK Computer (1997) [FLAC]", Categories: []newznab.CategoryID{3040}},
			kind: commonv1.MediaKindAlbum, parsedTitle: "Radiohead - OK Computer", artist: "Radiohead", album: "OK Computer",
		},
		{
			name: "the indexer's artist and album attrs win over the title's reading",
			in: torznab.Release{
				Title: "Radiohead - OK Computer OKNOTOK (1997) [FLAC]", Categories: []newznab.CategoryID{3040},
				Artist: "Radiohead", Album: "OK Computer",
			},
			kind: commonv1.MediaKindAlbum, parsedTitle: "Radiohead - OK Computer OKNOTOK", artist: "Radiohead", album: "OK Computer",
		},
		{
			name: "an ebook's author",
			in:   torznab.Release{Title: "Frank Herbert - Dune (1965) [EPUB]", Categories: []newznab.CategoryID{7020}},
			kind: commonv1.MediaKindBook, parsedTitle: "Dune", author: "Frank Herbert",
		},
		{
			name: "an author attr wins",
			in: torznab.Release{
				Title: "Herbert, Frank - Dune (1965) [EPUB]", Categories: []newznab.CategoryID{7020}, Author: "Frank Herbert",
			},
			kind: commonv1.MediaKindBook, parsedTitle: "Dune", author: "Frank Herbert",
		},
		{
			name: "an audiobook's author, from the narrator shape",
			in:   torznab.Release{Title: "The Stand - Stephen King {Grover Gardner} [ASIN B008TOJDNC] [M4B]"},
			kind: commonv1.MediaKindAudiobook, parsedTitle: "The Stand", author: "Stephen King",
		},
		{
			name: "a comic the classifier reads as a film is a comic by its category, issue and all",
			in:   torznab.Release{Title: "Batman 050 (2018) (Digital) (Zone-Empire)", Categories: []newznab.CategoryID{7000, 7030}},
			kind: commonv1.MediaKindComic, parsedTitle: "Batman", issue: "050",
		},
		{
			name: "a film carries no non-video name, whatever attrs the indexer sent",
			in: torznab.Release{
				Title: "The Matrix 1999 1080p BluRay x264-GROUP", Categories: []newznab.CategoryID{2040},
				Artist: "noise", Author: "noise",
			},
			kind: commonv1.MediaKindMovie, parsedTitle: "The Matrix",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.in.GUID = "g"
			got := rss.ProjectRelease(c.in, "idx", "torrent")
			require.Equal(t, c.kind, got.Kind)
			require.Equal(t, c.parsedTitle, got.ParsedTitle)
			require.Equal(t, c.artist, got.Artist)
			require.Equal(t, c.album, got.Album)
			require.Equal(t, c.author, got.Author)
			require.Equal(t, c.issue, got.Issue)

			// The kind on the wire is still the one that steered the parse.
			parsed, err := release.Parse(c.in.Title, release.Options{Kind: got.Kind})
			require.NoError(t, err)
			require.Equal(t, parsed.Title, got.ParsedTitle)
		})
	}

	// Without its category the comic is still the classifier's film: the
	// category refines a guess, it never invents one.
	got := rss.ProjectRelease(torznab.Release{Title: "Batman 050 (2018) (Digital) (Zone-Empire)", GUID: "g"}, "idx", "torrent")
	require.Equal(t, commonv1.MediaKindMovie, got.Kind)
	require.Empty(t, got.Issue)
}
