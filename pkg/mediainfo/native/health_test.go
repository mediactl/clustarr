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

package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/internal/testmedia"
	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// zeroed copies path with each [from, to) share of its bytes zeroed: what a
// usenet transfer leaves where an article never arrived, since it writes
// the articles it has into a file preallocated to its full size.
func zeroed(t *testing.T, path string, shares ...[2]float64) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, s := range shares {
		from, to := int(s[0]*float64(len(b))), int(s[1]*float64(len(b)))
		clear(b[from:to])
	}
	out := filepath.Join(t.TempDir(), "holed-"+filepath.Base(path))
	require.NoError(t, os.WriteFile(out, b, 0o644))
	return out
}

// Fena: Pirate Princess S01E01 came off usenet with 17% of its articles
// never fetched (talos, 2026-11-17): the header probed as a whole 24-minute
// episode, and the holes were found only by eye, after the transcode had
// encoded what was left. Read end to end, such a file's streams all skip
// the same stretches; a whole one's skip none.
func TestHealthFindsTheHolesInAFilesBytes(t *testing.T) {
	p := testProber(t)
	spec := testmedia.Spec{
		Duration: 60 * time.Second,
		Video: []testmedia.Video{{
			Source: "testsrc2=size=320x180:rate=24", Options: map[string]string{"preset": "ultrafast", "g": "24"},
		}},
		Audio: []testmedia.Audio{{Source: "sine=frequency=440"}, {Source: "sine=frequency=880"}},
	}
	ctx := context.Background()
	for _, name := range []string{"clip.mkv", "clip.mp4"} {
		t.Run(name, func(t *testing.T) {
			whole := testmedia.Make(t, name, spec)
			h, err := p.Health(ctx, whole)
			require.NoError(t, err)
			require.True(t, h.Judged)
			require.Zero(t, h.Holes, "a whole file: %+v", h)
			require.InDelta(t, 60, h.Duration.Seconds(), 1)
		})
	}

	whole := testmedia.Make(t, "clip.mkv", spec)
	t.Run("holes", func(t *testing.T) {
		h, err := p.Health(ctx, zeroed(t, whole, [2]float64{0.20, 0.30}, [2]float64{0.55, 0.62}, [2]float64{0.80, 0.84}))
		require.NoError(t, err)
		require.True(t, h.Damaged(), "%+v", h)
		require.GreaterOrEqual(t, h.Holes, 3)
		require.Greater(t, h.Missing, 10*time.Second, "a fifth of the bytes is gone")
		require.Less(t, h.Missing, 40*time.Second)
		require.ErrorIs(t, h.Err(), mediainfo.ErrDamaged)
	})
	t.Run("cut short", func(t *testing.T) {
		st, err := os.Stat(whole)
		require.NoError(t, err)
		h, err := p.Health(ctx, testmedia.Truncate(t, whole, st.Size()/2))
		require.NoError(t, err)
		require.True(t, h.Damaged(), "%+v", h)
		require.InDelta(t, 30, h.Missing.Seconds(), 6)
	})
	t.Run("a context that ends leaves nothing known", func(t *testing.T) {
		done, cancel := context.WithCancel(ctx)
		cancel()
		_, err := p.Health(done, whole)
		require.Error(t, err)
	})
}

// A file with no sound, or one in a container whose timestamps may jump by
// design, is not judged.
func TestHealthJudgesOnlyWhatItCan(t *testing.T) {
	p := testProber(t)
	silent := testmedia.Make(t, "silent.mkv", testmedia.Spec{
		Duration: 2 * time.Second,
		Video:    []testmedia.Video{{Source: "testsrc2=size=160x90:rate=24", Options: map[string]string{"preset": "ultrafast"}}},
	})
	h, err := p.Health(context.Background(), silent)
	require.NoError(t, err)
	require.False(t, h.Judged)
	require.False(t, h.Damaged())
}
