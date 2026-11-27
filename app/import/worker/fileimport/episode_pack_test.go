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

package fileimport

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gosickEpisodes are Gosick's first season as talos holds it (2026-11-27):
// the titles are the metadata provider's, which are not the pack's.
func gosickEpisodes() []EpisodeCandidate {
	var out []EpisodeCandidate
	for i, title := range []string{
		"The Black Reaper Finds the Golden Fairy", "The Souls of the Dead Raise a Shipwreck",
		"The Souls of the Dead Raise the Sunken Ship", "The Golden Thread Shreds the Moment",
		"There Is a Mysterious Ghost in the Abandoned Warehouse", "The Gray Wolf Calls to Its Kin",
		"A Divine Revelation Is Given on the Solstice", "Howls Echo in the Kingdom of the Past",
		"Blue Roses Bloom in the Man-Eating Department Store", "The Cold-Catcher Dreams About a Stubborn Friend",
		"His Drill Speaks Eloquently of Love", "The Cicadas' Hum Rings Out on a Summer Afternoon",
		"The Fool Names His Own Spokesman", "The Ill-Tempered Frill Condemns the Farting New",
		"Two Monsters Form a Bond", "The Fly-Headed Mary Caused the Fall", "The Box Rests In The Spiral Labyrinth",
		"The Obsidian Train Carries Numerous Lies", "The Rosy Life Is Buried Under a New Snow",
		"Guided by the Phantom of the Phantom", "The Holy Night Bell Drives the Hour Onward",
		"A Christmas Carol Hangs Happiness by the Window", "Checkmate Is Called on a Gray Chess Board",
		"Eternity Is Seen over the Reaper's Shoulder",
	} {
		out = append(out, EpisodeCandidate{Name: fmt.Sprintf("gosick-s01e%02d", i+1), Season: 1, Number: i + 1, Absolute: i + 1, Title: title})
	}
	return append(out, EpisodeCandidate{Name: "gosick-s00e01", Season: 0, Number: 1, Title: "Beautiful Monster"})
}

// gosickPack are the names of the pack held on talos, "Gosick [Dual Audio]
// [HEVC] [10-bit] [BDRip] [HD 1080p] [bluury]", without the three clips the
// sample rule left behind.
func gosickPack() []string {
	return []string{
		"Gosick 01 The Dark Reaper Finds the Golden Fairy", "Gosick 02 The Souls of the Dead Raise a Shipwreck",
		"Gosick 03 The Hares Break Promises Beneath the Morning Sun", "Gosick 04 The Golden Thread Cuts Through a Passing Moment",
		"Gosick 05 A Mysterious Ghost is in the Abandoned Warehouse", "Gosick 06 The Gray Wolves Summon Their Brethren",
		"Gosick 07 A Divine Revelation is Given at the Summer Solstice Festival", "Gosick 08 Howling Echoes from the Kingdom of the Past",
		"Gosick 09 A Blue Rose Blooms in a Cannibal Department Store", "Gosick 10 Girl With Cold Dreams of Her Stubborn Friend",
		"Gosick 11 The Drill Speaks Eloquently of Love", "Gosick 12 Listening to the Cicadas on a Summer Afternoon",
		"Gosick 13 A Fool Designates His Own Mouthpiece", "Gosick 14 A Malicious Frill Denounces a Farting Newt",
		"Gosick 15 Two Monsters See Eye to Eye", "Gosick 16 The Toppled Maria has a Fly's Head",
		"Gosick 17 The Box Lies in the Spiral Labyrinth", "Gosick 18 The Jet-Black Train Carries Several Lies",
		"Gosick 19 The Rose-Colored Life is Covered in New Snow", "Gosick 20 Guided by the Phantom's Ghost",
		"Gosick 21 The Bells of Cristmas Eve Toll at the Heels of Time", "Gosick 22 A Christmas Carol Decorates the Windowsill's Happiness",
		"Gosick 23 Call Checkmate on an Ash-Colored Chessboard", "Gosick 24 Looking at Infinity over the Grim Reaper's Shoulder",
	}
}

// A season pack's files are read from what is known of their download
// (owner, 2026-11-27: "Since we have the episode names and numbers, we
// should be able to build each filename ... We know the season when
// downloading a season pack"): the Gosick pack's twenty-four names, whose
// episode titles are another translation's than the metadata's, are read
// by the number where the names part. One title is the same in both, the
// second episode's, and agrees with its number.
func TestAPacksFilesAreReadByTheNumberWhereTheirNamesPart(t *testing.T) {
	got := packEpisodes(gosickPack(), gosickEpisodes(), 1)
	require.Len(t, got, 24)
	for i, name := range gosickPack() {
		assert.Equal(t, i+1, got[name], name)
	}

	// With no title to go by at all, a name shortened past the series'
	// own, and tags after the number.
	short := []string{"[Grp] Gsk - 01 [BD 1080p 5.1][AB12CD34]", "[Grp] Gsk - 02 [BD 1080p 5.1][0F0F0F0F]", "[Grp] Gsk - 10v2 [BD 1080p 5.1][12345678]"}
	assert.Equal(t, map[string]int{short[0]: 1, short[1]: 2, short[2]: 10}, packEpisodes(short, gosickEpisodes(), 1))

	// A number an episode's own title holds is not the one read.
	hours := []string{"Gosick 01 24 Hours of Rain", "Gosick 02 3 Wishes"}
	assert.Equal(t, map[string]int{hours[0]: 1, hours[1]: 2}, packEpisodes(hours, gosickEpisodes(), 1))

	// A pack counted through the series: season 2's episodes 1 to 3 are
	// the series' 25th to 27th.
	var second []EpisodeCandidate
	for i := 1; i <= 3; i++ {
		second = append(second, EpisodeCandidate{Season: 2, Number: i, Absolute: 24 + i})
	}
	counted := []string{"Show - 25", "Show - 26", "Show - 27"}
	assert.Equal(t, map[string]int{counted[0]: 1, counted[1]: 2, counted[2]: 3}, packEpisodes(counted, second, 2))
}

// What is not read: a number the season has no episode for, two files of
// one number, a name without a number where the others have one, a hash,
// and a download whose season nothing says. Each leaves every file a
// person's to place.
func TestAPackIsNotReadWhereItsNumbersDoNotFitTheSeason(t *testing.T) {
	eps := gosickEpisodes()
	assert.Nil(t, packEpisodes([]string{"Gosick 01 x", "Gosick 25 y"}, eps, 1), "the season has no 25th episode")
	assert.Nil(t, packEpisodes([]string{"Gosick 01 x", "Gosick 01 y"}, eps, 1), "two files of one number")
	assert.Nil(t, packEpisodes([]string{"Gosick 01 x", "Gosick Extras"}, eps, 1), "one name carries no number")
	assert.Nil(t, packEpisodes([]string{"Gosick 2011 x", "Gosick 2012 y"}, eps, 1), "a year is no episode")
	assert.Nil(t, packEpisodes([]string{"5e21e3e90faf4b1c9d7a6e5f4d3c2b1a", "00e74c75a59a4b1c9d7a6e5f4d3c2b1a"}, eps, 1), "a hash is no name")
	assert.Nil(t, packEpisodes(gosickPack(), eps, 0), "nothing says which season the download is of")
	assert.Nil(t, packEpisodes(gosickPack(), eps, 3), "the series has no such season")
	assert.Nil(t, packEpisodes([]string{"Gosick 01 x"}, eps, 1), "one file alone parts from nothing")
}

// A name that holds an episode's title is that episode's, with no number
// at all; of two titles it holds, the longer; and where the title and the
// number name different episodes nothing is read, for any file.
func TestAPacksFileIsReadByItsEpisodesTitle(t *testing.T) {
	eps := gosickEpisodes()
	named := []string{"The Fool Names His Own Spokesman", "Gosick - Two Monsters Form a Bond [BD 1080p]"}
	assert.Equal(t, map[string]int{named[0]: 13, named[1]: 15}, packEpisodes(named, eps, 1))
	assert.Equal(t, map[string]int{named[0]: 13}, packEpisodes(named[:1], eps, 1), "one file alone is read by its title")

	longer := append(eps, EpisodeCandidate{Season: 1, Number: 25, Title: "Two Monsters"})
	assert.Equal(t, 15, packEpisodes(named, longer, 1)[named[1]], "the longer title is the one the name holds whole")

	// The number says the second episode, the title the thirteenth.
	contradicting := []string{"Gosick 01 The Black Reaper Finds the Golden Fairy", "Gosick 02 The Fool Names His Own Spokesman"}
	assert.Nil(t, packEpisodes(contradicting, eps, 1))

	// A title too short to search for, and a provider's placeholder, name nothing.
	vague := []EpisodeCandidate{{Season: 1, Number: 1, Title: "Run"}, {Season: 1, Number: 2, Title: "Episode 2"}, {Season: 1, Number: 3, Title: "TBA"}}
	assert.Nil(t, packEpisodes([]string{"Brunch Episode 2 TBA"}, vague, 1))

	// Two files of one episode by their titles.
	assert.Nil(t, packEpisodes([]string{"A - The Fool Names His Own Spokesman", "B - The Fool Names His Own Spokesman"}, eps, 1))
}
