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

package worker

import (
	"encoding/binary"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

const graftClip = 120 // seconds: four alignment windows

// bursts writes seconds of seeded random tone bursts, 48 kHz mono WAV.
func bursts(t *testing.T, seed uint64, seconds float64) string {
	t.Helper()
	const rate = 48000
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	x := make([]float64, int(seconds*rate))
	for at := 0.0; at < seconds; at += 0.1 + r.Float64()*0.3 {
		f, amp, start := 200+r.Float64()*2800, 0.2+r.Float64()*0.5, int(at*rate)
		for i := 0; i < rate/4 && start+i < len(x); i++ {
			x[start+i] += amp * math.Exp(-float64(i)/(rate/20)) * math.Sin(2*math.Pi*f*float64(i)/rate)
		}
	}
	b := make([]byte, 44+2*len(x))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+2*len(x)))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], rate)
	binary.LittleEndian.PutUint32(b[28:], rate*2)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(2*len(x)))
	for i, v := range x {
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(int16(max(-1, min(1, v))*32000)))
	}
	p := filepath.Join(t.TempDir(), "b"+strconv.FormatUint(seed, 10)+".wav")
	require.NoError(t, os.WriteFile(p, b, 0o644))
	return p
}

// The joined-graft envtests ran on the TranscodeJob-keyed worker fixture
// F9.2 deleted (0beb88d6^:app/transcode/worker/worker_envtest_test.go). T2
// rebuilds that fixture on a MediaFile and ports these two from this file at
// 10f1a491 (joinedGraftFixture, ffmpegRun and the bodies), probing through
// pkg/mediainfo/native and building the task with jobspec.BuildTask's
// MediaFile signature (W8.5).

// TestATranscodeCarriesAJoinedGraftInOnePass (phase 4 addendum): a graft
// riding along with a transcode is aligned before the encode and muxed in
// with it -- one pass, one rewrite of the file: HEVC, the Japanese, the
// English dub, both tags.
func TestATranscodeCarriesAJoinedGraftInOnePass(t *testing.T) {
	t.Skip("T2: TestATranscodeCarriesAJoinedGraftInOnePass -- needs the MediaFile-keyed worker fixture (F9.2 rebuild)")
}

// TestAJoinedGraftThatWillNotAlignLeavesTheTranscodeAlone: a bad donor
// never costs the transcode.
func TestAJoinedGraftThatWillNotAlignLeavesTheTranscodeAlone(t *testing.T) {
	t.Skip("T2: TestAJoinedGraftThatWillNotAlignLeavesTheTranscodeAlone -- needs the MediaFile-keyed worker fixture (F9.2 rebuild)")
}
