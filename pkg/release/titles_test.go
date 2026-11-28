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

package release

import (
	"strings"
	"testing"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// An episode's release names its series twice as a pack's does: the
// romanised name with the English one in parentheses, or either side of a
// bar. Read as one title, 40 of the 53 releases a sample of 20 season
// searches refused for their title were the series under such a name
// (talos, 2026-11-27).
func TestAnEpisodesTitleCarriesItsSecondName(t *testing.T) {
	for title, want := range map[string][]string{
		"[Passerby] Nanatsu no Taizai: Mokushiroku no Yonkishi (The Seven Deadly Sins: Four Knights of the Apocalypse) - 25 [BD926B26].mkv": {
			"The Seven Deadly Sins: Four Knights of the Apocalypse",
		},
		"[Anitsu] Dungeon Meshi (Delicious in Dungeon) - 07 [BD 1080p x265 10-bit]": {"Delicious in Dungeon"},
	} {
		p, err := Parse(title, Options{Kind: commonv1.MediaKindEpisode})
		if err != nil {
			t.Fatalf("%s: %v", title, err)
		}
		for _, w := range want {
			found := false
			for _, got := range p.Titles {
				if strings.EqualFold(strings.TrimSpace(got), w) {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: titles %q lack %q", title, p.Titles, w)
			}
		}
	}
	// A year or a country in parentheses is no second name.
	p, err := Parse("Kingdom (2019) S01E02 1080p WEB-DL", Options{Kind: commonv1.MediaKindEpisode})
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range p.Titles {
		if strings.TrimSpace(got) == "2019" {
			t.Errorf("titles %q carry the year as a name", p.Titles)
		}
	}
}
