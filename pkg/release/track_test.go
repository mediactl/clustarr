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

package release_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/release"
)

func TestParseTrackFileName(t *testing.T) {
	cases := []struct {
		name string
		want release.TrackName
		ok   bool
	}{
		{"01 - Intro.flac", release.TrackName{Number: 1, Title: "Intro"}, true},
		{"02. Apocalypse Please.mp3", release.TrackName{Number: 2, Title: "Apocalypse Please"}, true},
		{"03 Time Is Running Out.flac", release.TrackName{Number: 3, Title: "Time Is Running Out"}, true},
		{"/data/music/Muse/Absolution/04 - Sing for Absolution.flac", release.TrackName{Number: 4, Title: "Sing for Absolution"}, true},
		{"Muse - Absolution - 05 - Stockholm Syndrome.flac", release.TrackName{Number: 5, Title: "Stockholm Syndrome"}, true},
		{"Muse - 06 - Falling Away with You.flac", release.TrackName{Number: 6, Title: "Falling Away with You"}, true},
		{"Track 7.m4a", release.TrackName{Number: 7, Title: ""}, true},
		{"1-02 Apocalypse Please.flac", release.TrackName{Disc: 1, Number: 2, Title: "Apocalypse Please"}, true},
		{"2.01 Hysteria.flac", release.TrackName{Disc: 2, Number: 1, Title: "Hysteria"}, true},
		{"D1T02 Title.flac", release.TrackName{Disc: 1, Number: 2, Title: "Title"}, true},
		{"CD2 03 Blackout.flac", release.TrackName{Disc: 2, Number: 3, Title: "Blackout"}, true},
		{"12 - 3 Hours.flac", release.TrackName{Number: 12, Title: "3 Hours"}, true},
		{"101 - Title.flac", release.TrackName{Number: 101, Title: "Title"}, true},
		// No number: the title's own digits never count.
		{"Summer of 69.mp3", release.TrackName{}, false},
		{"Intro.flac", release.TrackName{}, false},
		{"1999 - Prince.flac", release.TrackName{}, false},
		{"", release.TrackName{}, false},
		{"cover.jpg", release.TrackName{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := release.ParseTrackFileName(c.name)
			assert.Equal(t, c.ok, ok)
			if ok {
				assert.Equal(t, c.want, got)
			}
		})
	}
}
