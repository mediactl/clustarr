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

package chromaprint_test

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/segments/chromaprint"
)

func fixture(t testing.TB, name string) ([]int16, []uint32) {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "test", "data", "segments")
	raw, err := os.ReadFile(filepath.Join(dir, name+".s16le"))
	require.NoError(t, err)
	pcm := make([]int16, len(raw)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(raw[2*i:]))
	}
	fp, err := os.ReadFile(filepath.Join(dir, name+".fp"))
	require.NoError(t, err)
	want := make([]uint32, len(fp)/4)
	for i := range want {
		want[i] = binary.LittleEndian.Uint32(fp[4*i:])
	}
	return pcm, want
}

// The port must reproduce the reference bit for bit: the fixtures'
// fingerprints come from the media image's own ffmpeg chromaprint muxer.
func TestFingerprintMatchesFFmpegBitForBit(t *testing.T) {
	for _, name := range []string{"sweep", "chords", "noise"} {
		t.Run(name, func(t *testing.T) {
			pcm, want := fixture(t, name)
			got := chromaprint.Fingerprint(pcm)
			require.Len(t, got, len(want))
			diff := 0
			for i := range want {
				if got[i] != want[i] {
					diff++
				}
			}
			require.Zero(t, diff, "%d of %d points differ", diff, len(want))
		})
	}
}

// Ten minutes of audio -- the intro window's limit -- must fingerprint in
// well under a second on one core: a season of 24 episodes is decoded and
// fingerprinted for every new episode's comparison.
func BenchmarkFingerprintTenMinutes(b *testing.B) {
	pcm, _ := fixture(b, "chords")
	long := make([]int16, 0, 600*chromaprint.SampleRate)
	for len(long) < cap(long) {
		long = append(long, pcm[:min(len(pcm), cap(long)-len(long))]...)
	}
	b.ResetTimer()
	for range b.N {
		chromaprint.Fingerprint(long)
	}
}
