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

package mediainfo

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProbeHash(t *testing.T) {
	mtime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	got := ProbeHash("/data/movies/Foo/Foo.mkv", 123456789, mtime)
	assert.Equal(t, "a9d82f8f4ea18a3f034b611e0122b9e23b8b5981", got)

	// Any of the three inputs changing must change the hash -- this is the
	// whole point of MediaFileStatus's "replan on probeHash change" contract.
	assert.NotEqual(t, got, ProbeHash("/data/movies/Foo/Foo.mkv", 123456790, mtime))
	assert.NotEqual(t, got, ProbeHash("/data/movies/Bar/Bar.mkv", 123456789, mtime))
	assert.NotEqual(t, got, ProbeHash("/data/movies/Foo/Foo.mkv", 123456789, mtime.Add(time.Second)))
}

func TestMovieHash(t *testing.T) {
	dir := t.TempDir()

	writeFile := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, data, 0o644))
		return p
	}

	// All-zero file, exactly 2*osChunk bytes: first and last 64 KiB span
	// the whole file with no overlap and no gap, so the expected hash is
	// hand-computable: sum = uint64(size) + 0 + 0 = 131072 = 0x20000.
	zeros := writeFile("zeros.bin", make([]byte, 2*osChunk))

	// All-0x01 file, same size: every little-endian uint64 word is
	// 0x0101010101010101; sum = size + 16384*word, mod 2^64.
	onesBuf := make([]byte, 2*osChunk)
	for i := range onesBuf {
		onesBuf[i] = 0x01
	}
	ones := writeFile("ones.bin", onesBuf)

	// All-zero, 300000 bytes: every chunk is zero, so the hash equals the
	// size itself (Task B8's vector, adapted to this package's
	// MovieHash(path string) (string, error) signature).
	bigZeros := writeFile("big-zeros.bin", make([]byte, 300000))

	// 200000 bytes, buf[i] = i % 256: offset 134464 (the start of the
	// last 64 KiB) has pattern phase 134464 % 256 = 64, so this exercises
	// the little-endian summation for real, not just an all-same-byte
	// shortcut (Task B8's vector).
	patternBuf := make([]byte, 200000)
	for i := range patternBuf {
		patternBuf[i] = byte(i % 256)
	}
	pattern := writeFile("pattern.bin", patternBuf)

	tiny := writeFile("tiny.bin", make([]byte, 100))

	// One byte short of 2*osChunk (131071 bytes): still too small
	// (Task B8's vector).
	oneShort := writeFile("one-short.bin", make([]byte, 2*osChunk-1))

	tests := []struct {
		name    string
		path    string
		want    string
		wantErr error
	}{
		{name: "all zero, exactly two chunks", path: zeros, want: "0000000000020000"},
		{name: "all 0x01, exactly two chunks", path: ones, want: "4040404040424000"},
		{name: "all zero, 300000 bytes", path: bigZeros, want: "00000000000493e0"},
		{name: "byte(i%256) pattern, 200000 bytes", path: pattern, want: "a0601fdf9f620d40"},
		{name: "tiny file", path: tiny, wantErr: ErrTooSmall},
		{name: "one byte short of 2*osChunk", path: oneShort, wantErr: ErrTooSmall},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MovieHash(tc.path)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestMovieHashMissingFileReturnsError(t *testing.T) {
	_, err := MovieHash(filepath.Join(t.TempDir(), "does-not-exist.bin"))
	assert.Error(t, err)
}
