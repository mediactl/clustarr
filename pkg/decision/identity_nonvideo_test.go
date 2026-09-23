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

package decision_test

import (
	"testing"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
)

// nonVideoCase is one row of a non-video identity table. Only the identity
// verdict is asserted: identityProfile is a video profile, so a non-video
// release is rejected on quality whatever its identity, and assertIdentity
// looks at the identity check's rejections alone.
type nonVideoCase struct {
	name     string
	identity *decision.Identity // nil means the table's default
	title    string
	want     string // "" = no identity rejection, else the Reason code
	detail   string
}

func runNonVideo(t *testing.T, kind common.MediaKind, def decision.Identity, cases []nonVideoCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := def
			if tc.identity != nil {
				id = *tc.identity
			}
			tg := decision.Target{Kind: kind, Available: true, Identity: id}
			assertIdentity(t, evaluateOne(t, tg, tc.title, textIndexer, nil), tc.want, tc.detail)
		})
	}
}

// TestIdentityAlbum is Lidarr's rule: the artist is one of the item's, the
// album title is the item's, and the year is within five years.
func TestIdentityAlbum(t *testing.T) {
	kindOfBlue := decision.Identity{Titles: []string{"Kind of Blue"}, Creators: []string{"Miles Davis"}, Year: 1959}
	chk := decision.Identity{Titles: []string{"Wallop"}, Creators: []string{"!!!", "Chk Chk Chk"}, Year: 2019}
	onlySymbols := decision.Identity{Titles: []string{"Wallop"}, Creators: []string{"!!!"}, Year: 2019}
	noArtist := decision.Identity{Titles: []string{"Kind of Blue"}, Year: 1959}

	runNonVideo(t, common.MediaKindAlbum, kindOfBlue, []nonVideoCase{
		{name: "the album", title: "Miles Davis - Kind of Blue (1959) [FLAC]"},
		{name: "a reissue within five years", title: "Miles Davis - Kind of Blue (1964) [FLAC]"},
		{name: "a reissue beyond five years", title: "Miles Davis - Kind of Blue (1997) [FLAC]", want: "WrongItem", detail: "release year 1997 is more than 5 years from the item's 1959"},
		{name: "another album by the artist", title: "Miles Davis - Sketches of Spain (1960) [FLAC]", want: "WrongItem", detail: `release album title "Sketches of Spain" matches none of the item's 1 known titles (primary "Kind of Blue")`},
		{name: "the same title by another artist", title: "John Coltrane - Kind of Blue (1959) [FLAC]", want: "WrongItem", detail: `release artist "John Coltrane" matches none of the item's 1 known artists (primary "Miles Davis")`},
		{name: "a featured artist credit names the artist", title: "Miles Davis feat. John Coltrane - Kind of Blue (1959) [FLAC]"},
		{name: "an artist known by an alias", identity: &chk, title: "Chk Chk Chk - Wallop (2019) [FLAC]"},
		{name: "an unreadable artist fails closed", identity: &chk, title: "!!! - Wallop (2019) [FLAC]", want: "UnknownItem", detail: `release artist "!!!" has nothing the comparison can read`},
		{name: "an item artist with nothing readable fails closed", identity: &onlySymbols, title: "Chk Chk Chk - Wallop (2019) [FLAC]", want: "UnknownItem", detail: `none of the item's artists (primary "!!!") has anything the comparison can read`},
		{name: "a readable mismatch beats an unreadable half", title: "!!! - Sketches of Spain (1960) [FLAC]", want: "WrongItem", detail: `release album title "Sketches of Spain"`},
		{name: "an item with no artist yet identifies nothing", identity: &noArtist, title: "Miles Davis - Kind of Blue (1959) [FLAC]", want: "UnknownItem", detail: "the item has no artist yet"},
	})
}

// TestIdentityBook is Readarr's rule for a book and an audiobook: the author
// is one of the item's and the title is the item's; no year.
func TestIdentityBook(t *testing.T) {
	dune := decision.Identity{Titles: []string{"Dune"}, Creators: []string{"Frank Herbert"}, Year: 1965}
	goodOmens := decision.Identity{Titles: []string{"Good Omens"}, Creators: []string{"Terry Pratchett", "Neil Gaiman"}}
	noTitle := decision.Identity{Creators: []string{"Frank Herbert"}}

	cases := []nonVideoCase{
		{name: "the book", title: "Frank Herbert - Dune (1965) [EPUB]"},
		{name: "a later edition's year is not checked", title: "Frank Herbert - Dune (2005) [EPUB]"},
		{name: "the author filed Last, First", title: "Herbert, Frank - Dune (1965) [EPUB]"},
		{name: "another book by the author", title: "Frank Herbert - Dune Messiah (1969) [EPUB]", want: "WrongItem", detail: `release title "Dune Messiah" matches none of the item's 1 known titles (primary "Dune")`},
		{name: "the same title by another author", title: "Brian Herbert - Dune (1965) [EPUB]", want: "WrongItem", detail: `release author "Brian Herbert" matches none of the item's 1 known authors (primary "Frank Herbert")`},
		{name: "a co-written book names one of its authors", identity: &goodOmens, title: "Neil Gaiman & Terry Pratchett - Good Omens (1990) [EPUB]"},
		{name: "an item with no title yet identifies nothing", identity: &noTitle, title: "Frank Herbert - Dune (1965) [EPUB]", want: "UnknownItem", detail: "the item has no title yet"},
	}
	t.Run("book", func(t *testing.T) { runNonVideo(t, common.MediaKindBook, dune, cases) })
	t.Run("audiobook", func(t *testing.T) {
		runNonVideo(t, common.MediaKindAudiobook, dune, append(cases,
			nonVideoCase{name: "the narrator form", title: "Dune - Frank Herbert {Scott Brick} [ASIN B002V8KZ1A] [M4B]"},
			nonVideoCase{name: "the narrator form, another book", title: "Children of Dune - Frank Herbert {Scott Brick} [ASIN B002V8KZ1B] [M4B]", want: "WrongItem", detail: `release title "Children of Dune"`},
		))
	})
}

// TestIdentityIssue is Mylar's rule: the series is the issue's comic, the
// issue number is the issue's, and the year is within one of its cover year.
func TestIdentityIssue(t *testing.T) {
	batman50 := decision.Identity{Titles: []string{"Batman"}, Issue: "50", Year: 2018}
	onePiece := decision.Identity{Titles: []string{"One Piece"}, Issue: "1088", Year: 2023}
	noNumber := decision.Identity{Titles: []string{"Batman"}, Year: 2018}
	decimal := decision.Identity{Titles: []string{"Batman"}, Issue: "50.50", Year: 2018}

	runNonVideo(t, common.MediaKindIssue, batman50, []nonVideoCase{
		{name: "the issue, zero-padded", title: "Batman 050 (2018) (Digital) (Zone-Empire).cbr"},
		{name: "a year either side of the cover year", title: "Batman 050 (2019).cbz"},
		{name: "two years off the cover year", title: "Batman 050 (2016).cbz", want: "WrongItem", detail: "release year 2016 is more than 1 year from the item's 2018"},
		{name: "the next issue", title: "Batman 051 (2018).cbz", want: "WrongItem", detail: "release is issue 051, the item is issue 50"},
		{name: "a fractional item number is not the whole one", identity: &decimal, title: "Batman 050 (2018).cbz", want: "WrongItem", detail: "release is issue 050, the item is issue 50.50"},
		{name: "the same number of another series", title: "Superman 050 (2018).cbz", want: "WrongItem", detail: `release series "Superman" matches none of the item's 1 known titles (primary "Batman")`},
		{name: "a manga chapter", identity: &onePiece, title: "One Piece v107 c1088 (2023).cbz"},
		{name: "another manga chapter", identity: &onePiece, title: "One Piece v107 c1089 (2023).cbz", want: "WrongItem", detail: "release is issue 1089, the item is issue 1088"},
		{name: "an item with no number identifies nothing", identity: &noNumber, title: "Batman 050 (2018).cbz", want: "UnknownItem", detail: "the item has no issue number"},
		{name: "a wrong series beats a missing number", identity: &noNumber, title: "Superman 050 (2018).cbz", want: "WrongItem", detail: `release series "Superman"`},
	})
}
