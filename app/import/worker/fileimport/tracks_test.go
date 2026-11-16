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
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/metadata"
)

var twoDiscs = []catalogv1alpha1.Track{
	{RecordingID: "d1t1", Medium: 1, Number: 1, Title: "Intro"},
	{RecordingID: "d1t2", Medium: 1, Number: 2, Title: "Apocalypse Please"},
	{RecordingID: "d2t1", Medium: 2, Number: 1, Title: "Hysteria"},
	{RecordingID: "d2t2", Medium: 2, Number: 2, Title: "Blackout"},
}

func tagged(disc, track int32, rec string) *mediainfo.AudioProbe {
	return &mediainfo.AudioProbe{Codec: "flac", Tags: &commonv1.MediaTags{DiscNumber: disc, TrackNumber: track, MusicBrainzRecordingID: rec}}
}

// The tags decide first, the name second, the recording id third; a lone
// file is a one-track album's track; ambiguity and a miss are held for a
// person, never guessed (2026-11-10 design D6).
func TestAttributeTrack(t *testing.T) {
	cases := []struct {
		name   string
		tracks []catalogv1alpha1.Track
		media  int32
		ap     *mediainfo.AudioProbe
		rel    string
		lone   bool
		want   string
		held   bool
	}{
		{"tags name the disc and track", twoDiscs, 2, tagged(2, 1, ""), "whatever.flac", false, "d2t1", false},
		{"tags without a disc on a two-disc release are ambiguous, the folder decides", twoDiscs, 2, tagged(0, 1, ""), "CD2/01 - Hysteria.flac", false, "d2t1", false},
		{"tags without a disc and no folder: ambiguous", twoDiscs, 2, tagged(0, 1, ""), "01 - Hysteria.flac", false, "", true},
		{"the name decides when the tags carry no number", twoDiscs, 2, &mediainfo.AudioProbe{Codec: "mp3"}, "2-02 Blackout.mp3", false, "d2t2", false},
		{"the name's number on a one-disc release", twoDiscs[:2], 1, nil, "02 - Apocalypse Please.flac", false, "d1t2", false},
		{"the recording id decides when tags and name do not", twoDiscs, 2, tagged(0, 0, "D2T2"), "track.flac", false, "d2t2", false},
		{"a lone file is a one-track album's track whatever its name", twoDiscs[:1], 1, nil, "obfuscated.flac", true, "d1t1", false},
		{"a lone file of a two-track album is still attributed by its name", twoDiscs[:2], 1, nil, "02 - Apocalypse Please.flac", true, "d1t2", false},
		{"a number off the release is held", twoDiscs[:2], 1, tagged(1, 7, ""), "07 - Bonus.flac", false, "", true},
		{"no tracks: held", nil, 0, tagged(1, 1, ""), "01.flac", false, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, reason := AttributeTrack(c.tracks, c.media, c.ap, c.rel, c.lone)
			assert.Equal(t, c.want, id)
			assert.Equal(t, c.held, reason != "", reason)
		})
	}
}

// A release that lists no tracks yet answers ErrTracksPending, and only it:
// a library scan counts such a file as awaiting its album's tracks rather
// than unmatched (2026-11-10 design D17), so a file of a listed release that
// names no track must not read as pending.
func TestAttributeTrackSaysWhenTheTracksArePending(t *testing.T) {
	_, reason := AttributeTrack(nil, 0, tagged(1, 1, ""), "01.flac", false)
	assert.Equal(t, ErrTracksPending, reason)
	_, reason = AttributeTrack([]catalogv1alpha1.Track{}, 1, nil, "01.flac", true)
	assert.Equal(t, ErrTracksPending, reason, "an empty listing is pending too")

	_, reason = AttributeTrack(twoDiscs[:2], 1, tagged(1, 7, ""), "07 - Bonus.flac", false)
	assert.NotEmpty(t, reason)
	assert.NotEqual(t, ErrTracksPending, reason, "a track off a listed release is held, not pending")
}

func TestPartIndexOf(t *testing.T) {
	assert.EqualValues(t, 1003, PartIndexOf(tagged(1, 3, ""), "x.m4b"))
	assert.EqualValues(t, 2001, PartIndexOf(tagged(2, 1, ""), "x.m4b"))
	assert.EqualValues(t, 1005, PartIndexOf(tagged(0, 5, ""), "x.m4b"), "no disc reads as disc 1")
	assert.EqualValues(t, 7, PartIndexOf(nil, "The Left Hand of Darkness - Part 07.mp3"))
	assert.EqualValues(t, 0, PartIndexOf(&mediainfo.AudioProbe{}, "The Left Hand of Darkness.m4b"))
}

func titled(disc, track int32, title string) *mediainfo.AudioProbe {
	return &mediainfo.AudioProbe{Codec: "flac", Tags: &commonv1.MediaTags{DiscNumber: disc, TrackNumber: track, Title: title}}
}

// A title tag agrees with a track's title through the spellings rips and
// MusicBrainz differ by -- case, a featured-artist credit, "&" for "and",
// brackets, curly apostrophes, accents, a stylised run-together title, any
// script -- and never across what makes another recording: a cappella, a
// part two, another song (talos, 2026-11-16, the owner's scene rips).
func TestTitlesAgree(t *testing.T) {
	for _, c := range []struct {
		tag, track string
		want       bool
	}{
		{"Pieces feat. Cornelia", "Pieces", true},
		{"So Much (Feat. Philthy Rich, Stevie Joe & Ampichino)", "So Much", true},
		{"What Cha Day About (Prod. Qur'an Goodman)", "What Cha Day About", true},
		{"Millitant Part 2", "Millitant, Pt. 2", true},
		{"The Paw Paw Negro Blowtorch (2004 Digital Remaster)", "The Paw Paw Negro Blowtorch", true},
		{"Opus 4", "Opus 4 (2017 remastered version)", true},
		{"Song - Remastered 2011", "Song", true},
		{"May The Music Never Die Again (Japan Bonus Track)", "May the Music Never Die Again", true},
		{"?", "?", true},
		{"...", "?", false},
		{"Kush Coma ft. A$AP Rocky (Remix)", "Kush Coma", false},
		{"Song feat. X - Live", "Song", false},
		{"Song Ft X (Acoustic)", "Song Ft Y (Remix)", false},
		{"6 Ft 7 Ft", "6 Ft Under", false},
		{"Song (With Love)", "Song", false},
		{"née-nah (with Travis Scott & Metro Boomin)", "née-nah", false},
		{"Song (Remix)", "Song", false},
		{"Snitches & Rats (Interlude)", "Snitches & Rats (interlude)", true},
		{"Snitches and Rats (Interlude)", "Snitches & Rats (interlude)", true},
		{"HOLLERTILYOUPASSOUT", "Holler Til You Pass Out", true},
		{"R U Still Down? [Remember Me]", "R U Still Down? (Remember Me)", true},
		{"Can't C Me", "Can’t C Me", true},
		{"初恋", "初恋", true},
		{"Money Conversations", "Money Conversations (a cappella)", false},
		{"Move", "Move, Pt. 2", false},
		{"DROP", "GO FIRST", false},
		{"初恋", "花束を君に", false},
		{"", "Intro", false},

		// Version notes naming the album's own recording, and credits with a
		// colon or a co-star (talos, 2026-11-16: ~75 of 160 held files).
		{"He'll Never Know (Album Version)", "He’ll Never Know", true},
		{"Club - Skit (Album Version (Explicit))", "Club (skit)", true},
		{"Choke Me, Spank Me (Pull My Hair) (Explicit Version)", "Choke Me, Spank Me (Pull My Hair)", true},
		{"Song (Explicit)", "Song", true},
		{"Song [Clean Version]", "Song", true},
		{"Individual Breath (Original Mix)", "Individual Breath", true},
		{"Blind Fold (Produced by: BandPlay)", "Blind Fold", true},
		{"Do It Again (Co-Starring Keisa) (Produced By: Mr. Lee)", "Do it Again", true},
		{"Say You Do (Album Version)", "Say You Do (Specially Remixed Version)", false},
		{"Song (Single Version)", "Song", false},
		{"Song (Radio Edit)", "Song", false},
		{"Song (John Peel Session)", "Song", false},
		{"Cock It (Album Version (Explicit))", "Slow Motion", false},
	} {
		assert.Equal(t, c.want, TitlesAgree(c.tag, c.track), "%q vs %q", c.tag, c.track)
	}
}

// deluxe24 is a deluxe release whose numbering is not the standard
// edition's: the file numbered 10 on the 13-track release is track 20 here
// (DaBaby's BLAME IT ON BABY, talos, 2026-11-16).
var deluxe24 = []catalogv1alpha1.Track{
	{RecordingID: "billboard-baby", Medium: 1, Number: 1, Title: "BILLBOARD BABY"},
	{RecordingID: "go-first", Medium: 1, Number: 10, Title: "GO FIRST"},
	{RecordingID: "drop", Medium: 1, Number: 20, Title: "DROP"},
}

// A file's track number never attributes it to a track its title tag says
// it is not: the title finds the track on the release instead, or the file
// is held for a person, never renamed to another song.
func TestAttributeTrackNeverTakesANumberThatNamesAnotherSong(t *testing.T) {
	id, reason := AttributeTrack(deluxe24, 1, titled(0, 10, "DROP"), "10.DaBaby - DROP-TDS.flac", false)
	assert.Equal(t, "drop", id, "the title finds the song the number does not: %s", reason)

	acappella := []catalogv1alpha1.Track{
		{RecordingID: "water-ac", Medium: 1, Number: 5, Title: "Water (Drowning Pt. 2) (a cappella)"},
		{RecordingID: "money-ac", Medium: 1, Number: 6, Title: "Money Conversations (a cappella)"},
	}
	id, reason = AttributeTrack(acappella, 1, titled(0, 5, "Money Conversations"), "05-a_boogie_wit_da_hoodie-money_conversations.flac", false)
	assert.Empty(t, id, "an a cappella edition holds no track the file is")
	assert.Contains(t, reason, `"Money Conversations"`)
	assert.Contains(t, reason, `"Water (Drowning Pt. 2) (a cappella)"`)

	id, _ = AttributeTrack(deluxe24, 1, titled(0, 10, "Go First"), "10.flac", false)
	assert.Equal(t, "go-first", id, "a title that agrees keeps the number's track")
	id, _ = AttributeTrack(deluxe24, 1, tagged(0, 10, ""), "10.flac", false)
	assert.Equal(t, "go-first", id, "a file without a title tag is still attributed by its number")

	american := []catalogv1alpha1.Track{{RecordingID: "nee-nah", Medium: 1, Number: 9, Title: "née-nah"}, {RecordingID: "other", Medium: 1, Number: 1, Title: "Other"}}
	id, _ = AttributeTrack(american, 1, titled(0, 9, "née-nah (with Travis Scott & Metro Boomin)"), "09.flac", false)
	assert.Equal(t, "nee-nah", id, "a bracketed with-credit is dropped where the number agrees too")
	id, _ = AttributeTrack(american, 1, titled(0, 4, "née-nah (with Travis Scott & Metro Boomin)"), "04.flac", false)
	assert.Empty(t, id, "the title alone never drops a with-bracket, which may be the title's own ((With Love))")

	single := []catalogv1alpha1.Track{{RecordingID: "the-single", Medium: 1, Number: 1, Title: "The Single"}}
	id, _ = AttributeTrack(single, 1, titled(0, 7, "Another Song"), "07.flac", true)
	assert.Empty(t, id, "a one-track album's lone file whose title is another song is not its track")
	id, _ = AttributeTrack(single, 1, titled(0, 0, ""), "obfuscated.flac", true)
	assert.Equal(t, "the-single", id, "a lone file saying nothing is a one-track album's track")

	remix := []catalogv1alpha1.Track{{RecordingID: "song", Medium: 1, Number: 3, Title: "Song"}, {RecordingID: "other", Medium: 1, Number: 1, Title: "Other"}}
	id, _ = AttributeTrack(remix, 1, titled(0, 15, "Song feat. X (Remix)"), "15.flac", false)
	assert.Empty(t, id, "a remix is never attributed to the original by its title")
}

// vinylOld is Danny Brown's Old as the 4x12" vinyl clustarr selected: 38
// tracks numbered per side, so no track 18 (talos, 2026-11-16). digitalOld
// is the 19-track digital release the owner's rip is.
var vinylOld = []catalogv1alpha1.Track{
	{RecordingID: "side-a-old", Medium: 1, Number: 1, Title: "Side A (Old)"},
	{RecordingID: "kush-coma-vinyl", Medium: 4, Number: 8, Title: "Kush Coma"},
}

func digitalOld() []metadata.AlbumRelease {
	tracks := []metadata.Track{
		{IDs: metadata.ExternalIDs{metadata.KeyMBRecording: "side-a-old"}, Title: "Side A (Old)", Position: 1, MediumNumber: 1},
		{IDs: metadata.ExternalIDs{metadata.KeyMBRecording: "kush-coma"}, Title: "Kush Coma", Position: 18, MediumNumber: 1},
	}
	return []metadata.AlbumRelease{
		{IDs: metadata.ExternalIDs{metadata.KeyMBRelease: "vinyl"}, TrackCount: 38, Media: []metadata.Medium{
			{Position: 1, Tracks: []metadata.Track{{IDs: metadata.ExternalIDs{metadata.KeyMBRecording: "side-a-old"}, Title: "Side A (Old)", Position: 1, MediumNumber: 1}}},
			{Position: 4, Tracks: []metadata.Track{{IDs: metadata.ExternalIDs{metadata.KeyMBRecording: "kush-coma-vinyl"}, Title: "Kush Coma", Position: 8, MediumNumber: 4}}},
		}},
		{IDs: metadata.ExternalIDs{metadata.KeyMBRelease: "digital"}, TrackCount: 19, Media: []metadata.Medium{{Position: 1, Tracks: tracks}}},
	}
}

// A file the selected release does not number is attributed in the album's
// release whose number and title it fits -- its own edition -- before any
// title alone is trusted: Kush Coma is track 18 of the digital Old, which
// the vinyl the album selected numbers nowhere. The releases are asked for
// only when the selected one does not settle the file.
func TestAttributeTrackInTakesTheEditionTheFileFits(t *testing.T) {
	asked := 0
	releases := func() []metadata.AlbumRelease { asked++; return digitalOld() }

	id, reason := AttributeTrackIn(vinylOld, 4, releases, titled(0, 18, "Kush Coma"), "18_danny_brown-kush_coma_ft_asap_rocky_and_zelooperz-fih.mp3", false)
	assert.Equal(t, "kush-coma", id, "the digital release numbers it 18 and names it Kush Coma: %s", reason)
	assert.Equal(t, 1, asked)

	asked = 0
	id, _ = AttributeTrackIn(vinylOld, 4, releases, titled(1, 1, "Side A (Old)"), "01.mp3", false)
	assert.Equal(t, "side-a-old", id)
	assert.Zero(t, asked, "a file the selected release settles asks for no other release")

	id, reason = AttributeTrackIn(vinylOld, 4, func() []metadata.AlbumRelease { return nil }, titled(0, 18, "Kush Coma"), "18.mp3", false)
	assert.Equal(t, "kush-coma-vinyl", id, "without the other releases the title finds the selected release's own track: %s", reason)

	id, reason = AttributeTrackIn(vinylOld, 4, releases, titled(0, 18, "Not On Old"), "18.mp3", false)
	assert.Empty(t, id)
	assert.NotEmpty(t, reason, "a song no release has is held")
}
