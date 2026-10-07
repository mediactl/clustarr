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

package ffprobeexec

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/mediainfo"
)

func TestProbeAudioReadsTheFixtures(t *testing.T) {
	skipIfNoFFprobe(t)
	ctx := context.Background()

	mp3, err := ProbeAudio(ctx, "../../../test/data/mediainfo/audio_mp3_cbr320.mp3")
	require.NoError(t, err)
	assert.Equal(t, mediainfo.AudioProbe{Codec: "mp3", BitrateKbps: 320}, mp3)

	flac, err := ProbeAudio(ctx, "../../../test/data/mediainfo/audio_flac_24bit.flac")
	require.NoError(t, err)
	assert.Equal(t, "flac", flac.Codec)
	assert.Equal(t, 24, flac.SampleBits)

	_, err = ProbeAudio(ctx, "../../../test/data/mediainfo/does-not-exist.mp3")
	require.Error(t, err)
}
