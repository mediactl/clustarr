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

// HDR10ClipBakedPath is where images/Dockerfile.e2e-fixtures's HDR10
// clipgen stage places its generated clip in the final image (Task E-5:
// HEVC 10-bit, BT.2020/PQ, mastering-display and content-light metadata,
// built with the identical ffmpeg recipe
// pkg/mediainfo/hdr10_fixture_test.go proves classifies as HDR10). That unit
// test is this clip's whole proof obligation -- no scenario in test/e2e
// consumes it as of Phase E, deliberately (see test/e2e/transcode_test.go's
// package doc comment for why scenario 12 uses the plain probe clip
// instead) -- Run copies it out anyway so a future HDR-aware scenario has a
// real, ffprobe-verified HDR10 source ready without a second bake step.
const HDR10ClipBakedPath = "/fixtures/media/hdr10.mkv"

// HDR10ClipName is the basename Run writes under its destination directory
// when HDR10ClipBakedPath is present.
const HDR10ClipName = "hdr10.mkv"

// TorznabDirName is the subdirectory of the seed directory that the
// torznab-stub appends its request log to. It is created world-writable
// here, by the HOST user, because the stub pod runs as uid/gid 1000 and a
// hostPath mount ignores fsGroup -- there is no other moment at which a
// process with the right identity touches this path.
const TorznabDirName = "torznab"

// RequestLogName is the JSONL file inside it.
const RequestLogName = "requests.jsonl"

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

	// The HDR10 clip is optional cargo. Its absence must not fail Run --
	// tiny.mkv above is what checkDataDir gates the whole suite on, and an
	// image built before Task E-5 simply has no /fixtures/media/hdr10.mkv --
	// but if the baked path exists and cannot be copied, that is a real
	// image defect worth failing loudly on, exactly like tiny.mkv.
	if _, err := os.Stat(HDR10ClipBakedPath); err == nil {
		if err := copyFile(HDR10ClipBakedPath, filepath.Join(dir, HDR10ClipName)); err != nil {
			return fmt.Errorf("seed: copy HDR10 clip: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("seed: stat HDR10 clip: %w", err)
	}

	reqDir := filepath.Join(dir, TorznabDirName)
	if err := os.MkdirAll(reqDir, 0o777); err != nil {
		return fmt.Errorf("seed: mkdir %s: %w", reqDir, err)
	}
	// MkdirAll applies the process umask, which under §11's UMASK 002 clears
	// the other-write bit that the stub pod depends on. Chmod does not.
	if err := os.Chmod(reqDir, 0o777); err != nil {
		return fmt.Errorf("seed: chmod %s: %w", reqDir, err)
	}
	return nil
}

// copyFile copies src to dst with mode 0664, matching the group-writable
// convention every planted file in this package uses (§11's UMASK 002).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o664)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s -> %s: %w", src, dst, err)
	}
	return out.Close()
}
