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
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// ProbeHash is sha1(path|size|mtime), matching
// api/catalog/v1alpha1.MediaFileStatus.ProbeHash's doc comment: a change
// in any of the three makes downstream services (transcode compliance,
// subtitle sidecar rescans) replan.
func ProbeHash(path string, size int64, mtime time.Time) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d", path, size, mtime.UnixNano())))
	return hex.EncodeToString(sum[:])
}

// osChunk is the OpenSubtitles moviehash chunk size: the first and last
// 64 KiB of the file (docs/research/subtitles.md §4.4).
const osChunk = 64 * 1024

// ErrTooSmall is returned by MovieHash for files under 2*osChunk (128
// KiB): docs/research/subtitles.md §4.4 notes the upstream Python
// reference implementation rejects them outright.
var ErrTooSmall = errors.New("mediainfo: file smaller than 128 KiB, cannot compute movie hash")

// MovieHash is OpenSubtitles' moviehash: filesize plus the sum of the
// first and last 64 KiB read as little-endian uint64 words, wrapping mod
// 2^64, printed as 16 lowercase hex digits (docs/research/subtitles.md §4.4).
func MovieHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("mediainfo: movie hash: %w", err)
	}
	defer func() { _ = f.Close() }()

	st, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("mediainfo: movie hash: %w", err)
	}
	if st.Size() < 2*osChunk {
		return "", ErrTooSmall
	}

	sum := uint64(st.Size())
	buf := make([]byte, osChunk)
	for _, off := range []int64{0, st.Size() - osChunk} {
		if _, err := f.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("mediainfo: movie hash: %w", err)
		}
		for i := 0; i < osChunk; i += 8 {
			sum += binary.LittleEndian.Uint64(buf[i:])
		}
	}
	return fmt.Sprintf("%016x", sum), nil
}
