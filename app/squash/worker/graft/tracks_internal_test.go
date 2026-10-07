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

package graft

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/transcode/engine"
)

// A surround dub adds two tracks (AC-3 5.1 and its AAC 2.0 companion), a
// stereo one one: what the transcode's expectation and the graft check
// count.
func TestAPreparedGraftCountsItsTracks(t *testing.T) {
	assert.Equal(t, 2, (&Prepared{audio: engine.GraftAudio{Surround: true}}).Tracks())
	assert.Equal(t, 1, (&Prepared{audio: engine.GraftAudio{}}).Tracks())
}

// A donor track of more than two channels is a surround dub.
func TestSurroundIsMoreThanTwoChannels(t *testing.T) {
	assert.True(t, surround(engine.Track{Channels: 6}))
	assert.True(t, surround(engine.Track{Channels: 8}))
	assert.False(t, surround(engine.Track{Channels: 2}))
	assert.False(t, surround(engine.Track{Channels: 1}))
}
