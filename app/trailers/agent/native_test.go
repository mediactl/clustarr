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

package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/internal/testmedia"
	"github.com/mediactl/clustarr/pkg/mediainfo/native"
	"github.com/mediactl/clustarr/pkg/transcode/engine"
)

// The fetch's own join and read, on real files: a picture-only and a
// sound-only fragmented MP4, as a trailer's site serves them, become one
// MP4 the prober reads as a video with sound -- what Sweeper.fetch requires
// before it places anything.
func TestAFetchedPairIsJoinedAndReadAsAVideoWithSound(t *testing.T) {
	testmedia.Require(t)
	prober, err := native.New()
	require.NoError(t, err)
	fragmented := map[string]string{"movflags": "+frag_keyframe+empty_moov+default_base_moof"}
	video := testmedia.Make(t, "137.mp4", testmedia.Spec{
		Format: "mp4", MuxerOptions: fragmented, Duration: 3 * time.Second,
		Video: []testmedia.Video{{Source: "testsrc2=size=320x180:rate=24", Options: map[string]string{"preset": "ultrafast"}}},
	})
	audio := testmedia.Make(t, "140.m4a", testmedia.Spec{
		Format: "mp4", MuxerOptions: fragmented, Duration: 3 * time.Second,
		Audio: []testmedia.Audio{{Source: "sine=frequency=440"}},
	})
	out := filepath.Join(t.TempDir(), "trailer.mp4")

	require.NoError(t, engine.MuxPair(context.Background(), video, audio, out))
	facts, err := probeWith(prober)(context.Background(), out)
	require.NoError(t, err)
	assert.Equal(t, "h264", facts.VideoCodec)
	assert.Equal(t, "aac", facts.AudioCodec)
	assert.Equal(t, 320, facts.Width)
	assert.Equal(t, 180, facts.Height)
	assert.InDelta(t, 3000, facts.DurationMillis, 200)
	fi, err := os.Stat(out)
	require.NoError(t, err)
	assert.Equal(t, fi.Size(), facts.SizeBytes)
}
