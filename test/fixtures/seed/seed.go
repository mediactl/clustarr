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

// Package seed copies the lavfi-generated clip baked into the fixture image
// (by images/Dockerfile.e2e-fixtures's ffmpeg build stage) onto the shared
// /data volume, so test/e2e can "plant" ffprobe-able media by a plain
// filesystem copy -- no ffmpeg needed on the machine running `go test`, and
// catalogarr's MediaFile controller probes real bytes with real ffprobe,
// never a mock.
//
// # Why the clip is not tiny
//
// pkg/fsops.IsSample flags any file with a MediaExtensions extension under
// 50 MiB as a promotional sample, and the rescan worker skips everything
// fsops does not classify as ClassMedia. A genuinely tiny clip would
// therefore never become a MediaFile, so the baked clip is deliberately
// encoded CBR to land just over that threshold. MinMediaBytes records the
// contract; [Run] refuses to seed a clip that would be classified as a
// sample rather than letting every scenario fail later with an empty
// catalog.
package seed

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// BakedClipPath is where images/Dockerfile.e2e-fixtures places the
// generated clip in the final image.
const BakedClipPath = "/fixtures/media/tiny.mkv"

// ClipName is the basename Run writes under its destination directory.
const ClipName = "tiny.mkv"

// MinMediaBytes is pkg/fsops's sampleMaxBytes: at or above it a .mkv is
// ClassMedia, below it ClassSample. It is duplicated rather than imported
// because this package is also the image's runtime entrypoint and must not
// drag the controller packages in, and because a silent drift is exactly
// what the check below is for.
const MinMediaBytes = 50 * 1024 * 1024

// Run copies BakedClipPath to <dir>/tiny.mkv, creating dir if needed. The
// mode is group-writable: hack/kind.sh's /data is shared with pods running
// as uid/gid 1000 under UMASK 002.
func Run(dir string) error {
	if err := os.MkdirAll(dir, 0o775); err != nil {
		return fmt.Errorf("seed: mkdir %s: %w", dir, err)
	}
	src, err := os.Open(BakedClipPath)
	if err != nil {
		return fmt.Errorf("seed: open baked clip: %w", err)
	}
	defer func() { _ = src.Close() }()

	info, err := src.Stat()
	if err != nil {
		return fmt.Errorf("seed: stat baked clip: %w", err)
	}
	if info.Size() < MinMediaBytes {
		return fmt.Errorf(
			"seed: baked clip is %d bytes, under pkg/fsops's %d-byte sample threshold; "+
				"every planted file would be classified as a sample and skipped",
			info.Size(), MinMediaBytes)
	}

	dst := filepath.Join(dir, ClipName)
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o664)
	if err != nil {
		return fmt.Errorf("seed: create %s: %w", dst, err)
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, src); err != nil {
		return fmt.Errorf("seed: copy: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("seed: close %s: %w", dst, err)
	}
	return nil
}
