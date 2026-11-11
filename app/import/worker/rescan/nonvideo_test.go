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

package rescan_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/rescan"
)

const root = "/data/media/lib"

// verdict is the part of an ItemMatch a table row asserts on.
type verdict struct {
	name string // matched item; "" when unmatched
	code string // reason code when unmatched
}

func check(t *testing.T, got rescan.ItemMatch, want verdict) {
	t.Helper()
	if want.name != "" {
		assert.False(t, got.Unmatched, "want a match, got %s: %s", got.Code, got.Reason)
		assert.Equal(t, want.name, got.Ref.Name)
		return
	}
	assert.True(t, got.Unmatched, "the scanner never guesses: want unmatched, got %s/%s", got.Ref.Kind, got.Ref.Name)
	assert.Equal(t, want.code, got.Code)
	assert.NotEmpty(t, got.Reason, "an unmatched file must carry a reason")
}

func TestMatchAlbum(t *testing.T) {
	okc := rescan.AlbumCandidate{Name: "ok-computer", Title: "OK Computer", Year: 1997, ArtistNames: []string{"Radiohead"}}
	kida := rescan.AlbumCandidate{Name: "kid-a", Title: "Kid A", Year: 2000, ArtistNames: []string{"Radiohead"}}
	cands := []rescan.AlbumCandidate{okc, kida}

	for _, tc := range []struct {
		desc  string
		rel   string
		cands []rescan.AlbumCandidate
		want  verdict
	}{
		{"pkg/naming layout", "Radiohead/OK Computer (1997)/01 - Airbag.flac", cands, verdict{name: "ok-computer"}},
		{"disc subfolder", "Radiohead/OK Computer (1997)/CD1/01 - Airbag.flac", cands, verdict{name: "ok-computer"}},
		{"folder without a year", "Radiohead/Kid A/01.flac", cands, verdict{name: "kid-a"}},
		{"release-shaped album folder", "Radiohead/Radiohead - OK Computer (1997) [FLAC]/01.flac", cands, verdict{name: "ok-computer"}},
		{"cleaned punctuation and case", "radiohead/ok computer! (1997)/01.flac", cands, verdict{name: "ok-computer"}},
		{"an artist folder's mbid token is set aside", "Radiohead [mbid-a74b1b7f-71a5-4011-9441-d0b5e4122711]/Kid A/01.flac", cands, verdict{name: "kid-a"}},

		// Every "must not match" case.
		{"wrong year", "Radiohead/OK Computer (1998)/01.flac", cands, verdict{code: rescan.CodeNoMatch}},
		{"right album, wrong artist", "Radiohed/OK Computer (1997)/01.flac", cands, verdict{code: rescan.CodeNoMatch}},
		{"unknown album", "Radiohead/Amnesiac (2001)/01.flac", cands, verdict{code: rescan.CodeNoMatch}},
		// D17: a flat artist folder is attributed by its tags; with none
		// and no album folder it keeps the layout reason.
		{"a flat file with no tags", "Radiohead/01 - Airbag.flac", cands, verdict{code: rescan.CodeUnrecognisedLayout}},
		{"a file at the root names no artist", "01 - Airbag.flac", cands, verdict{code: rescan.CodeUnrecognisedLayout}},
		{
			"two albums share the names", "Radiohead/OK Computer/01.flac",
			[]rescan.AlbumCandidate{okc, {Name: "ok-computer-oknotok", Title: "OK Computer", ArtistNames: []string{"Radiohead"}}},
			verdict{code: rescan.CodeAmbiguousTitle},
		},
		{
			"artist has no metadata yet", "Radiohead/OK Computer (1997)/01.flac",
			[]rescan.AlbumCandidate{{Name: "ok-computer", Title: "OK Computer"}},
			verdict{code: rescan.CodeNoMatch},
		},
		{"no albums at all", "Radiohead/OK Computer (1997)/01.flac", nil, verdict{code: rescan.CodeNoMatch}},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			check(t, rescan.MatchAlbum(filepath.Join(root, tc.rel), tc.rel, nil, tc.cands), tc.want)
		})
	}

	t.Run("declared folder decides without names", func(t *testing.T) {
		declared := rescan.AlbumCandidate{Name: "renamed", Path: filepath.Join(root, "Whatever", "Folder")}
		got := rescan.MatchAlbum(filepath.Join(root, "Whatever/Folder/01.flac"), "Whatever/Folder/01.flac", nil,
			[]rescan.AlbumCandidate{okc, declared})
		check(t, got, verdict{name: "renamed"})
		assert.Equal(t, commonv1.MediaKindAlbum, got.Ref.Kind)
	})
}

// A track is attributed by its tags whatever the folder depth (2026-11-10
// design D17): the release-group id, then a release id of the group, then
// the album folder, then the ALBUM tag, each against the albums of the
// artist the first segment names.
func TestMatchAlbumByTags(t *testing.T) {
	const (
		okcGroup  = "b1392450-e666-3926-a536-22c65f834433"
		kidaGroup = "a9b4dbb3-4ea8-3a4a-9a5e-48e5c0a0dc3f"
		kidaUK    = "0ccbfe86-7d4c-4c1b-8b1c-97ec3d7d5e6b" // a release of Kid A's group
	)
	okc := rescan.AlbumCandidate{
		Name: "ok-computer", Title: "OK Computer", Year: 1997, ArtistNames: []string{"Radiohead"},
		ReleaseGroupID: okcGroup, ReleaseIDs: []string{"0b6b4ba0-d36f-47bd-b4ea-6a5b91842d29"},
	}
	kida := rescan.AlbumCandidate{
		Name: "kid-a", Title: "Kid A", Year: 2000, ArtistNames: []string{"Radiohead"},
		ReleaseGroupID: kidaGroup, ReleaseIDs: []string{"5b2c8c7d-2d1e-4d22-9b0a-3b2d7a5a6f01", kidaUK},
	}
	// Another artist's album of Kid A's title: never a candidate for a
	// file in Radiohead's folder.
	cover := rescan.AlbumCandidate{Name: "kid-a-cover", Title: "Kid A", ArtistNames: []string{"Somebody Else"}, ReleaseGroupID: "x"}
	cands := []rescan.AlbumCandidate{okc, kida, cover}

	for _, tc := range []struct {
		desc string
		rel  string
		tags *commonv1.MediaTags
		want verdict
	}{
		{"the release-group id", "Radiohead/track.flac", &commonv1.MediaTags{MusicBrainzReleaseGroupID: kidaGroup}, verdict{name: "kid-a"}},
		{
			"the release-group id before the album folder", "Radiohead/OK Computer (1997)/track.flac",
			&commonv1.MediaTags{MusicBrainzReleaseGroupID: kidaGroup},
			verdict{name: "kid-a"},
		},
		{"a release id of the group", "Radiohead/track.flac", &commonv1.MediaTags{MusicBrainzReleaseID: kidaUK}, verdict{name: "kid-a"}},
		{
			"ids naming no album pass to the next rule", "Radiohead/OK Computer/track.flac",
			&commonv1.MediaTags{MusicBrainzReleaseGroupID: "unknown", MusicBrainzReleaseID: "unknown"},
			verdict{name: "ok-computer"},
		},
		{"the album folder", "Radiohead/OK Computer (1997)/track.flac", &commonv1.MediaTags{Title: "Airbag"}, verdict{name: "ok-computer"}},
		{"the ALBUM tag in a flat folder", "Radiohead/03 - Exit Music.flac", &commonv1.MediaTags{Album: "OK Computer"}, verdict{name: "ok-computer"}},
		{
			"the ALBUM tag when the album folder names nothing", "Radiohead/Rips/01.flac",
			&commonv1.MediaTags{Album: "Kid A"},
			verdict{name: "kid-a"},
		},
		{"the tag's year narrows", "Radiohead/01.flac", &commonv1.MediaTags{Album: "Kid A", Date: "2000-10-02"}, verdict{name: "kid-a"}},

		{
			"the tag's year contradicts", "Radiohead/01.flac", &commonv1.MediaTags{Album: "Kid A", Date: "1999"},
			verdict{code: rescan.CodeNoChild},
		},
		{
			"an ALBUM tag the artist has no album of", "Radiohead/01.flac", &commonv1.MediaTags{Album: "I Might Be Wrong"},
			verdict{code: rescan.CodeNoChild},
		},
		{
			"another artist's release-group id", "Radiohead/01.flac", &commonv1.MediaTags{MusicBrainzReleaseGroupID: "x"},
			verdict{code: rescan.CodeUnrecognisedLayout},
		},
		{"tags naming no album, no album folder", "Radiohead/01.flac", &commonv1.MediaTags{Title: "Airbag"}, verdict{code: rescan.CodeUnrecognisedLayout}},
		{"no tags, no album folder", "Radiohead/01.flac", nil, verdict{code: rescan.CodeUnrecognisedLayout}},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			check(t, rescan.MatchAlbum(filepath.Join(root, tc.rel), tc.rel, tc.tags, cands), tc.want)
		})
	}

	t.Run("an ALBUM tag no album has names it", func(t *testing.T) {
		got := rescan.MatchAlbum(filepath.Join(root, "Radiohead/01.flac"), "Radiohead/01.flac",
			&commonv1.MediaTags{Album: "I Might Be Wrong"}, cands)
		assert.Contains(t, got.Reason, `no album of Radiohead is titled "I Might Be Wrong"`)
	})
	t.Run("two albums of one release group are ambiguous", func(t *testing.T) {
		twin := kida
		twin.Name = "kid-a-twin"
		got := rescan.MatchAlbum(filepath.Join(root, "Radiohead/01.flac"), "Radiohead/01.flac",
			&commonv1.MediaTags{MusicBrainzReleaseGroupID: kidaGroup}, []rescan.AlbumCandidate{kida, twin})
		check(t, got, verdict{code: rescan.CodeAmbiguousTitle})
		assert.Equal(t, []string{"kid-a", "kid-a-twin"}, got.Candidates)
	})
}

// MatchArtist names the existing artist of the root an artist folder is,
// in layers, never guessing, and never another root's.
func TestMatchArtist(t *testing.T) {
	const mbid = "a74b1b7f-71a5-4011-9441-d0b5e4122711"
	radiohead := rescan.ArtistCandidate{Name: "radiohead", MusicBrainzID: mbid, Names: []string{"Radiohead", "On a Friday"}}
	moved := rescan.ArtistCandidate{Name: "portishead", Path: "Portishead (Bristol)", Names: []string{"Portishead"}}
	pinned := rescan.ArtistCandidate{Name: "bjork", Folder: "Bjork", Names: []string{"Björk"}}
	elsewhere := rescan.ArtistCandidate{Name: "nirvana", Names: []string{"Nirvana"}, OtherRootFolder: "music-lossy"}
	cands := []rescan.ArtistCandidate{radiohead, moved, pinned, elsewhere}

	for _, tc := range []struct {
		folder string
		want   verdict
	}{
		{"Radiohead", verdict{name: "radiohead"}},
		{"On a Friday", verdict{name: "radiohead"}},
		{"Radiohead [mbid-" + mbid + "]", verdict{name: "radiohead"}},
		{"Portishead (Bristol)", verdict{name: "portishead"}},
		{"Bjork", verdict{name: "bjork"}},

		{"Nirvana", verdict{code: rescan.CodeNoMatch}},
		{"Radiohead {mbid-00000000-0000-0000-0000-000000000000}", verdict{code: rescan.CodeNoMatch}},
		{"Radiohed", verdict{code: rescan.CodeNoMatch}},
	} {
		t.Run(tc.folder, func(t *testing.T) {
			got := rescan.MatchArtist(tc.folder, cands)
			check(t, got, tc.want)
			if tc.want.name != "" {
				assert.Equal(t, commonv1.MediaKindArtist, got.Ref.Kind)
			}
		})
	}
	t.Run("two artists of one name are ambiguous", func(t *testing.T) {
		got := rescan.MatchArtist("Genesis", []rescan.ArtistCandidate{
			{Name: "genesis-uk", Names: []string{"Genesis"}}, {Name: "genesis-us", Names: []string{"Genesis"}},
		})
		check(t, got, verdict{code: rescan.CodeAmbiguousTitle})
	})
}

func TestMatchBook(t *testing.T) {
	dune := rescan.BookCandidate{Name: "dune", Title: "Dune", Year: 1965, AuthorNames: []string{"Frank Herbert"}}
	messiah := rescan.BookCandidate{Name: "dune-messiah", Title: "Dune Messiah", AuthorNames: []string{"Frank Herbert"}}
	solo := rescan.BookCandidate{Name: "solo", Title: "Flatland", Standalone: true}
	cands := []rescan.BookCandidate{dune, messiah, solo}

	for _, tc := range []struct {
		desc string
		rel  string
		want verdict
	}{
		{"pkg/naming layout", "Frank Herbert/Dune/Frank Herbert.epub", verdict{name: "dune"}},
		{"book folder with year", "Frank Herbert/Dune (1965)/Frank Herbert.epub", verdict{name: "dune"}},
		{"author/title file", "Frank Herbert/Dune Messiah.mobi", verdict{name: "dune-messiah"}},
		{"release-shaped name at the root", "Frank Herbert - Dune (1965) [EPUB].epub", verdict{name: "dune"}},
		{"standalone book: the author folder cannot contradict an unknown author", "Edwin Abbott/Flatland/x.pdf", verdict{name: "solo"}},

		{"wrong author", "Brian Herbert/Dune/x.epub", verdict{code: rescan.CodeNoMatch}},
		{"wrong year", "Frank Herbert/Dune (1984)/x.epub", verdict{code: rescan.CodeNoMatch}},
		{"unknown title", "Frank Herbert/Children of Dune/x.epub", verdict{code: rescan.CodeNoMatch}},
		{"bare file at the root", "Dune.epub", verdict{code: rescan.CodeUnrecognisedLayout}},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			check(t, rescan.MatchBook(tc.rel, cands), tc.want)
		})
	}
}

func TestMatchAudiobook(t *testing.T) {
	wizard := rescan.AudiobookCandidate{
		Name: "wizards-first-rule", ASIN: "B002V0QK4C", Year: 1994,
		Titles:  []string{"Wizard's First Rule", "Wizard's First Rule Sam Tsoutsouvas"},
		Authors: []string{"Terry Goodkind", "Terry Goodkind"},
	}
	messiah := rescan.AudiobookCandidate{
		Name: "dune-messiah", ASIN: "B002V1OF70", Year: 2007,
		Titles:  []string{"Dune Messiah", "Dune Messiah Scott Brick"},
		Authors: []string{"Frank Herbert", "Frank Herbert"},
	}
	cands := []rescan.AudiobookCandidate{wizard, messiah}

	for _, tc := range []struct {
		desc  string
		rel   string
		cands []rescan.AudiobookCandidate
		want  verdict
	}{
		{"Audiobookshelf bare ASIN", "Whoever/Anything [B002V0QK4C]/01.mp3", cands, verdict{name: "wizards-first-rule"}},
		{"pkg/release ASIN token", "X/Y [ASIN B002V1OF70]/01.m4b", cands, verdict{name: "dune-messiah"}},
		{
			"Audiobookshelf folder grammar", "Terry Goodkind/Sword of Truth/Vol 1 - 1994 - Wizard's First Rule {Sam Tsoutsouvas}/01.mp3",
			cands,
			verdict{name: "wizards-first-rule"},
		},
		{
			"pkg/naming preset, narrator without braces", "Frank Herbert/Dune/2 - 2007 - Dune Messiah Scott Brick/Part 1.m4b",
			cands,
			verdict{name: "dune-messiah"},
		},
		{"disc folders are skipped", "Frank Herbert/Dune Messiah/Disc 2/03.mp3", cands, verdict{name: "dune-messiah"}},

		// An embedded id is the identity. The title here matches
		// wizard exactly, and it must still not be attributed there.
		{
			"unknown ASIN never falls back to the title",
			"Terry Goodkind/Wizard's First Rule [B00UNKNOWN]/01.mp3", cands,
			verdict{code: rescan.CodeUnknownID},
		},
		{
			"one ASIN in two marketplaces is ambiguous", "A/B [B002V0QK4C]/01.mp3",
			[]rescan.AudiobookCandidate{wizard, {Name: "wizards-first-rule-uk", ASIN: "B002V0QK4C"}},
			verdict{code: rescan.CodeAmbiguousTitle},
		},
		{"wrong author", "Someone Else/Dune Messiah/01.mp3", cands, verdict{code: rescan.CodeNoMatch}},
		{"wrong year", "Frank Herbert/1999 - Dune Messiah/01.mp3", cands, verdict{code: rescan.CodeNoMatch}},
		{"too shallow", "Dune Messiah/01.mp3", cands, verdict{code: rescan.CodeUnrecognisedLayout}},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			check(t, rescan.MatchAudiobook(filepath.Join(root, tc.rel), tc.rel, tc.cands), tc.want)
		})
	}
}

func TestMatchIssue(t *testing.T) {
	comics := []rescan.ComicCandidate{
		{Name: "saga", Titles: []string{"Saga"}, Year: 2012},
		{Name: "paper-girls", Titles: []string{"Paper Girls"}, Year: 2015, Path: filepath.Join(root, "PG")},
	}
	issues := []rescan.IssueCandidate{
		{Name: "saga-00001.0", ComicRef: "saga", Number: "1", Centis: 100},
		{Name: "saga-00012.5", ComicRef: "saga", Number: "12.5", Centis: 1250},
		{Name: "saga-annual", ComicRef: "saga", Number: "Annual 1", Centis: 0},
		{Name: "pg-00003.0", ComicRef: "paper-girls", Number: "3", Centis: 300},
	}

	for _, tc := range []struct {
		desc string
		rel  string
		want verdict
	}{
		{"release-shaped issue", "Saga/Saga 001 (2012).cbz", verdict{name: "saga-00001.0"}},
		{"pkg/naming preset", "Saga/Saga c001.cbz", verdict{name: "saga-00001.0"}},
		{"decimal issue", "Saga/Saga c12.5.cbr", verdict{name: "saga-00012.5"}},
		{"series folder with year", "Saga (2012)/Saga 001 (2012).cbz", verdict{name: "saga-00001.0"}},
		{"declared comic folder", "PG/Paper Girls 003 (2016).cbz", verdict{name: "pg-00003.0"}},
		{"file at the root, series from the name", "Saga 001 (2012).cbz", verdict{name: "saga-00001.0"}},

		{"comic found, issue missing", "Saga/Saga 099 (2012).cbz", verdict{code: rescan.CodeNoChild}},
		{"unknown series", "Monstress/Monstress 001 (2015).cbz", verdict{code: rescan.CodeNoMatch}},
		{"series year contradicts", "Saga (1999)/Saga 001 (1999).cbz", verdict{code: rescan.CodeNoMatch}},
		{"no issue number", "Saga/Saga.cbz", verdict{code: rescan.CodeParseError}},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			check(t, rescan.MatchIssue(filepath.Join(root, tc.rel), tc.rel, comics, issues), tc.want)
		})
	}
}
