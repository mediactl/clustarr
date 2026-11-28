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

package decision

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// vouched judges title as an episode's release an indexer files under the
// series' id: carried in the release (carried), or by answering a query for
// it from the indexer "amenzb".
func vouched(t *testing.T, id Identity, tvdb, title string, carried bool) *common.Rejection {
	t.Helper()
	id.IDs = map[string]string{common.IDKeyTVDB: tvdb}
	id.IDQueryIndexers = map[string]bool{"amenzb": true}
	rel := common.ReleaseInfo{Title: title, IndexerRef: "amenzb"}
	if carried {
		rel.IDs = map[string]string{common.IDKeyTVDB: tvdb}
		rel.IndexerRef = "elsewhere"
	}
	p, err := release.Parse(title, release.Options{Kind: common.MediaKindEpisode})
	require.NoError(t, err)
	return itemRejection(common.MediaKindEpisode, id, targetTitleKeys(common.MediaKindEpisode, id), nil, p, rel)
}

// lupin is Lupin III (TVDB 71863) as talos named it on 2026-11-27.
func lupin() Identity {
	return Identity{Year: 1971, Titles: []string{
		"Lupin III", "ルパン三世", "Lupin III: Parte 1", "Lupin III: Parte 3", "Edgar de la Cambriole",
		"Lupin III Part I", "Lupin Sansei", "Lupin III Part III", "Lupin the 3rd Part 4", "Lupin the Third Part 5",
		"Lupin III (1971)", "Lupin the 3rd",
	}}
}

// An indexer's id does not carry a release whose title names something
// else. The 22 best-ranked answers for Lupin III's third season were the
// live-action "Lupin" of 2021, filed by an indexer under the anime's ids
// (talos, 2026-11-27; owner: "This obviously needs fixing"), and 17 of
// Unlimited Psychic Squad's were Takopi's Original Sin. Both ways an id
// vouches are held to it: carried by the release, and by answering an id
// query.
func TestAnIndexersIDDoesNotCarryATitleThatNamesSomethingElse(t *testing.T) {
	squad := Identity{Titles: []string{
		"Unlimited Psychic Squad", "THE UNLIMITED 兵部京介", "Courtesy of Zettai Karen Children: The Unlimited - Hyoubu Kyousuke",
	}}
	for _, tc := range []struct {
		id    Identity
		tvdb  string
		title string
		why   string
	}{
		{lupin(), "71863", "Lupin.S03E05.1080p.NF.WEB-DL.DUAL.DDP5.1.Atmos.H.264-XEBEC", "only the start of one of the item's names"},
		{lupin(), "71863", "Lupin.S03E01.1080p.NF.WEB-DL.HIN-TAM-TEL-ENG-FRE.DD+5.1.Atmos.H.264-playWEB", "only the start"},
		{lupin(), "71863", "Lupin (2021-2023) S03E02 1080p NF WEB-DL", "only the start"},
		{lupin(), "71863", "Game.On.S03E04.1080p.WEB.H264-GRP", "no word in common"},
		{squad, "264528", "Takopis.Original.Sin.S01E03.1080p.CR.WEB-DL.DUAL.AAC2.0.H.264-VARYG", "no word in common"},
	} {
		for _, carried := range []bool{true, false} {
			r := vouched(t, tc.id, tc.tvdb, tc.title, carried)
			require.NotNil(t, r, "%s (carried %v)", tc.title, carried)
			assert.True(t, strings.HasPrefix(r.Reason, ReasonWrongItem.Code+":"), r.Reason)
			assert.Contains(t, r.Reason, tc.why, tc.title)
		}
	}
}

// What the id is there for still passes on it: a name the series' titles do
// not list that shares a word with one, a name with a year, a season marker
// or a group left in it, a name less its catalogue's year, and a title in a
// script the comparison cannot read. Every title here matched none of its
// series' in talos' sample and was the series.
func TestAnIndexersIDStillCarriesANameTheTitlesDoNotList(t *testing.T) {
	squad := Identity{Titles: []string{
		"Unlimited Psychic Squad", "THE UNLIMITED 兵部京介", "Courtesy of Zettai Karen Children: The Unlimited - Hyoubu Kyousuke",
	}}
	madoka := Identity{Titles: []string{"Puella Magi Madoka Magica", "Mahou Shoujo Madoka Magica"}}
	kingdom := Identity{Year: 2019, Titles: []string{"Kingdom (2019)"}}
	knockout := Identity{Titles: []string{"Life with an Ordinary Guy Who Reincarnated into a Total Fantasy Knockout"}}
	for _, tc := range []struct {
		id    Identity
		tvdb  string
		title string
	}{
		{lupin(), "71863", "Lupin III S3 - 05 - We Are not Angels [neo1024][D0505173]"},
		{lupin(), "71863", "[Judas] Lupin the Third - S03E20"},
		{squad, "264528", "The.Unlimited-Hyoubu.Kyousuke.S01E04.1080p.BluRay.x264-GRP"},
		{madoka, "212671", "Puella.Magi.Madoka.Magica.2011.S01E03.1080p.BluRay.x265-GRP"},
		{madoka, "212671", "meow meow - Puella Magi Madoka Magica - 03 [1080p]"},
		{kingdom, "366532", "Kingdom.S01E02.1080p.NF.WEB-DL.DDP5.1.H.264-GRP"},
		{knockout, "408254", "[GM-Team] 与变成了异世界美少女的好友一起冒险 - 03 [1080p]"},
	} {
		for _, carried := range []bool{true, false} {
			assert.Nil(t, vouched(t, tc.id, tc.tvdb, tc.title, carried), "%s (carried %v)", tc.title, carried)
		}
	}
}

// A movie is left to its ids and its year: a film's release is named in
// many languages, and its ids are the indexer's own imdb or tmdb attribute.
func TestAMoviesIDIsNotHeldToItsTitle(t *testing.T) {
	id := Identity{Year: 1972, Titles: []string{"The Godfather"}, IDs: map[string]string{common.IDKeyIMDB: "tt0068646"}}
	title := "Der.Pate.1972.German.DL.1080p.BluRay.x264-GRP"
	p, err := release.Parse(title, release.Options{Kind: common.MediaKindMovie})
	require.NoError(t, err)
	rel := common.ReleaseInfo{Title: title, IDs: map[string]string{common.IDKeyIMDB: "tt0068646"}}
	assert.Nil(t, itemRejection(common.MediaKindMovie, id, targetTitleKeys(common.MediaKindMovie, id), nil, p, rel))
}
