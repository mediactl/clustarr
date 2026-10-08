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

// Command mkclips writes the e2e fixture clips with internal/testmedia on
// ffgo, not the ffmpeg CLI (owner decision 2026-10-06; upgrade guide U2.7,
// W10.11). images/Dockerfile.e2e-fixtures runs it in its clipgen stage on
// the native image's pinned FFmpeg, and the final image carries what it
// writes under /fixtures/media.
//
// # The probe clip is 1080p (ruling, 2026-10-08)
//
// tiny.mkv is 1920x1080, the resolution every name it travels under claims:
// the seeder's and the NNTP stub's "Clustarr.Fixture.2010.1080p.BluRay..."
// and every 1080p file test/e2e plants. It was 640x480 until the smoke test
// of 2026-10-07 found that both the import and the library rescan correct a
// name's resolution from the probe (quality.AugmentFromMediaInfo), so the
// "1080p" download imported as Bluray-480p, which config/e2e's e2e-any
// profile (HD only) refuses and blocklists. Naming the clip for what it was
// (480p) would have needed an SD tier in e2e-any and a 480p name in every
// scenario; making it what its names say keeps them. Planted names that
// claim another resolution (transcode_test.go's) say 1080p now, since one
// clip cannot be two resolutions.
//
// The bit rate is not an accident: pkg/fsops classifies a video file under
// 50 MiB as a suspected sample, so the clip is x264 CBR at 48 Mb/s for ten
// seconds, about 57 MiB whatever its frame size; main refuses to finish
// under the floor (test/fixtures/seed.MinMediaBytes asserts it again at
// seed time).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/obinnaokechukwu/ffgo/avdevice"

	"github.com/mediactl/clustarr/internal/testmedia"
)

// minMediaBytes is test/fixtures/seed.MinMediaBytes, the floor under which
// pkg/fsops reads a video file as a sample. It is restated, not imported,
// so this command links nothing but testmedia.
const minMediaBytes = 50 << 20

// probeClip is the frame size of tiny.mkv: the 1080p its names claim (see
// the package doc's ruling).
const probeClip = "testsrc=duration=10:size=1920x1080:rate=25"

// specs are the clips the image carries, as testmedia Specs: the same
// recipes the Dockerfile's ffmpeg commands were, tiny.mkv at 1080p.
func specs() map[string]testmedia.Spec {
	return map[string]testmedia.Spec{
		"tiny.mkv": {
			Shortest: true,
			Video: []testmedia.Video{{
				Source: probeClip, Encoder: "libx264", PixFmt: "yuv420p",
				Options: map[string]string{
					"preset": "ultrafast", "x264-params": "nal-hrd=cbr:force-cfr=1",
					"b": "48M", "minrate": "48M", "maxrate": "48M", "bufsize": "48M",
				},
			}},
			Audio: []testmedia.Audio{{Source: "sine=frequency=1000:duration=10", Encoder: "aac"}},
		},
		"hdr10.mkv": {
			Video: []testmedia.Video{{
				Source: "testsrc2=duration=3:size=640x480:rate=25", Encoder: "libx265", PixFmt: "yuv420p10le",
				Options: map[string]string{"x265-params": "log-level=error"},
				Color:   testmedia.Color{Primaries: "bt2020", Transfer: "smpte2084", Matrix: "bt2020nc"},
				HDR10: &testmedia.HDR10{
					MasteringDisplay: [10]uint32{13250, 34500, 7500, 3000, 34000, 16000, 15635, 16450, 10000000, 1},
					MaxCLL:           1000, MaxFALL: 400,
				},
			}},
		},
	}
}

func main() {
	out := flag.String("out", "/out", "where to write the clips")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "mkclips:", err)
		os.Exit(1)
	}
}

func run(out string) error {
	// lavfi sources open only after avdevice.RegisterAll: testmedia.Write
	// registers the devices once per process, but a failure here names the
	// cause before any clip is attempted.
	if err := avdevice.RegisterAll(); err != nil {
		return fmt.Errorf("libavdevice: %w", err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	all := specs()
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := testmedia.Write(filepath.Join(out, name), all[name]); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	st, err := os.Stat(filepath.Join(out, "tiny.mkv"))
	if err != nil {
		return err
	}
	if st.Size() < minMediaBytes {
		return fmt.Errorf("tiny.mkv is %d bytes; it must be at least %d (the sample floor the e2e suite crosses)", st.Size(), minMediaBytes)
	}
	return nil
}
