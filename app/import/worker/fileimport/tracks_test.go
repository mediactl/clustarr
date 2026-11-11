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
