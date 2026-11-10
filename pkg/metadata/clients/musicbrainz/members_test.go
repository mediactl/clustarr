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

package musicbrainz_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/musicbrainz"
)

// membersServer serves one recorded artist lookup (musicbrainz.org,
// 2026-11-10, inc=artist-rels+url-rels+ratings+genres+tags+annotation+
// aliases, stored whole) and records the inc parameter it was asked with.
func membersServer(t *testing.T, fixture string, inc *string) *musicbrainz.Client {
	t.Helper()
	body, err := os.ReadFile("../../../../test/data/metadata/musicbrainz/" + fixture)
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*inc = r.URL.Query().Get("inc")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	c, err := musicbrainz.New("Clustarr/0.1 (https://github.com/mediactl/clustarr)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)
	return c
}

// A group's "member of band" relations become its Members, one per person
// however many instruments MusicBrainz lists them under, each a founding
// member of Radiohead; its tribute acts, supporting musicians, subgroup and
// earlier name are no members, and Similar stays empty (2026-11-10 design
// §5.4; plan W2b.1 leftover).
func TestArtistMapsAGroupsMembersFromItsArtistRelations(t *testing.T) {
	var inc string
	c := membersServer(t, "artist_radiohead_members.json", &inc)

	a, err := c.Artist(context.Background(), "a74b1b7f-71a5-4011-9441-d0b5e4122711")
	require.NoError(t, err)
	require.Contains(t, inc, "artist-rels", "the members come from the artist-rels include")
	require.Contains(t, inc, "url-rels")

	require.Equal(t, "Group", a.Type)
	require.Empty(t, a.Similar, "MusicBrainz has no similar-artist relation")
	names := make([]string, 0, len(a.Members))
	for i, m := range a.Members {
		names = append(names, m.Name)
		require.Equal(t, metadata.PersonMember, m.Kind, m.Name)
		require.Equal(t, "founding member", m.Role, m.Name)
		require.EqualValues(t, i, m.Order, "Order is the listing order")
	}
	require.Equal(t, []string{"Colin Greenwood", "Ed O’Brien", "Jonny Greenwood", "Philip Selway", "Thom Yorke"}, names,
		"five members, by name when none has a begin date; Nigel Godrich (supporting musician), The Smile (subgroup), On a Friday (rename) and the tribute acts are not members")
	require.Equal(t, "lead vocals, acoustic guitar, electric guitar, synthesizer, piano", a.Members[4].Job,
		`one Person for Thom Yorke's five relations, the instruments in MusicBrainz's order and "original" read as the founding flag, not an instrument`)
	require.Equal(t, "percussion, drums (drum set)", a.Members[3].Job)
	require.Equal(t, metadata.ExternalIDs{metadata.KeyMBArtist: "8ed2e0b3-aa4c-4e13-bec3-dc7393ed4d6b"}, a.Members[4].IDs,
		"a member carries their own MusicBrainz artist id (task C4a)")
	require.Equal(t, metadata.ExternalIDs{metadata.KeyMBArtist: "f23074f4-2c06-477a-bf1d-12fa66e087ee"}, a.Members[0].IDs)
	for _, m := range a.Members {
		require.Nil(t, m.Begin, "%s: MusicBrainz dates none of Radiohead's memberships", m.Name)
		require.Nil(t, m.End, m.Name)
	}
}

// A person's lookup sees the same relations forward, so the groups they
// are in are the Members, worded "of": current groups first (founding ones
// named so), former ones after, the earliest joined first.
func TestArtistMapsAPersonsGroupsAsMembersOf(t *testing.T) {
	var inc string
	c := membersServer(t, "artist_thom_yorke_members.json", &inc)

	a, err := c.Artist(context.Background(), "8ed2e0b3-aa4c-4e13-bec3-dc7393ed4d6b")
	require.NoError(t, err)
	require.Equal(t, "Person", a.Type)

	type row struct{ name, role, job string }
	rows := make([]row, 0, len(a.Members))
	for _, m := range a.Members {
		require.Equal(t, metadata.PersonMember, m.Kind)
		rows = append(rows, row{m.Name, m.Role, m.Job})
	}
	require.Equal(t, []row{
		{"Atoms for Peace", "founding member of", ""},
		{"Radiohead", "founding member of", "lead vocals, acoustic guitar, electric guitar, synthesizer, piano"},
		{"The Smile", "member of", "guitar, lead vocals, piano, keyboard, bass guitar"},
		{"The Venus in Furs", "member of", ""},
		{"Headless Chickens", "former member of", ""},
		{"On a Friday", "former member of", ""},
	}, rows, "current groups by name, then the former ones with Headless Chickens' 1989 tenure ahead of On a Friday's undated one; the ended relation outranks the founding flag")
	require.NotContains(t, rows, row{"Rachel Owen", "", ""}, "a marriage is no membership")

	// The dates MusicBrainz records (task C4a): Headless Chickens 1989-1989,
	// On a Friday ended 1991 with no begin, and a current group has no End.
	byName := map[string]metadata.Person{}
	for _, m := range a.Members {
		byName[m.Name] = m
	}
	headless := byName["Headless Chickens"]
	require.NotNil(t, headless.Begin)
	require.Equal(t, time.Date(1989, 1, 1, 0, 0, 0, 0, time.UTC), *headless.Begin, "a year alone is January the 1st")
	require.NotNil(t, headless.End)
	require.Equal(t, time.Date(1989, 1, 1, 0, 0, 0, 0, time.UTC), *headless.End)
	require.Equal(t, metadata.ExternalIDs{metadata.KeyMBArtist: "2371ce0b-7990-4b88-af6f-6a8b01ef369a"}, headless.IDs)
	onAFriday := byName["On a Friday"]
	require.Nil(t, onAFriday.Begin, "MusicBrainz dates no begin")
	require.NotNil(t, onAFriday.End)
	require.Equal(t, time.Date(1991, 1, 1, 0, 0, 0, 0, time.UTC), *onAFriday.End)
	require.Nil(t, byName["Radiohead"].End, "a current membership has no end")
	require.Nil(t, byName["Radiohead"].Begin)
}

// An artist without artist-rels (the trimmed Radiohead fixture every older
// test reads) has no Members and is otherwise unchanged.
func TestArtistWithoutArtistRelationsHasNoMembers(t *testing.T) {
	var inc string
	c := membersServer(t, "artist_radiohead.json", &inc)

	a, err := c.Artist(context.Background(), "a74b1b7f-71a5-4011-9441-d0b5e4122711")
	require.NoError(t, err)
	require.Nil(t, a.Members)
	require.Contains(t, a.Links, metadata.Link{Type: "wikidata", URL: "https://www.wikidata.org/wiki/Q6033"})
}
