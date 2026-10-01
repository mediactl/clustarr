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

package inprocess

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

func ffmpegClip(t *testing.T, name string, args ...string) string {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("no %s to make or compare against", bin)
		}
	}
	out := filepath.Join(t.TempDir(), name)
	a := append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)
	if b, err := exec.Command("ffmpeg", append(a, out)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, b)
	}
	return out
}

// probeFixtures are one file per class the standard tells apart.
func probeFixtures(t *testing.T) map[string]string {
	dir := t.TempDir()
	srt, font, meta := filepath.Join(dir, "s.srt"), filepath.Join(dir, "f.ttf"), filepath.Join(dir, "c.ffmeta")
	require.NoError(t, os.WriteFile(srt, []byte("1\n00:00:00,500 --> 00:00:01,500\nHello\n"), 0o644))
	require.NoError(t, os.WriteFile(font, []byte("not really a font"), 0o644))
	require.NoError(t, os.WriteFile(meta, []byte(";FFMETADATA1\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=1000\ntitle=One\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=1000\nEND=2000\ntitle=Two\n"), 0o644))
	x265 := func(params string) []string {
		return []string{
			"-f", "lavfi", "-i", "testsrc2=duration=2:size=640x480:rate=25", "-c:v", "libx265", "-preset", "ultrafast",
			"-pix_fmt", "yuv420p10le", "-x265-params", "log-level=error:" + params,
		}
	}
	return map[string]string{
		"sdr h264, every stream kind": ffmpegClip(t, "everything.mkv",
			"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=2",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=2,aformat=channel_layouts=5.1",
			"-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000:duration=2",
			"-i", srt, "-i", meta, "-map", "0", "-map", "1", "-map", "2", "-map", "3", "-map_chapters", "4",
			"-c:v", "libx264", "-preset", "ultrafast", "-c:a:0", "ac3", "-c:a:1", "aac", "-c:s", "srt",
			"-attach", font, "-metadata:s:t", "mimetype=application/x-truetype-font",
			"-metadata:s:a:0", "language=eng", "-metadata:s:a:1", "language=fre", "-metadata:s:a:1", "title=Commentary",
			"-metadata:s:s:0", "language=eng", "-disposition:a:0", "default", "-disposition:a:1", "comment",
			"-disposition:s:0", "forced", "-metadata", "title=Clip", "-metadata", "CLUSTARR_PROFILE=p@h"),
		"hdr10": ffmpegClip(t, "hdr10.mkv", x265("colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:"+
			"master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1):max-cll=1000,400")...),
		"hlg":          ffmpegClip(t, "hlg.mkv", x265("colorprim=bt2020:transfer=arib-std-b67:colormatrix=bt2020nc")...),
		"hi10p":        ffmpegClip(t, "hi10p.mkv", "-f", "lavfi", "-i", "testsrc2=duration=2:size=320x180:rate=24", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le"),
		"mp4 h264 aac": ffmpegClip(t, "clip.mp4", "-f", "lavfi", "-i", "testsrc2=duration=2:size=320x180:rate=24", "-f", "lavfi", "-i", "sine=duration=2", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac"),
	}
}

// The worker's probe runs in-process (the transcoder image carries no
// ffprobe); it must read every file as pkg/mediainfo's ffprobe probe does,
// or the worker's plan and catalogarr's would differ.
func TestTheInProcessProbeAgreesWithFFprobe(t *testing.T) {
	ffmpeg9OrSkip(t)
	for name, path := range probeFixtures(t) {
		t.Run(name, func(t *testing.T) {
			want, wantRaw, err := mediainfo.Probe(context.Background(), path)
			require.NoError(t, err)
			got, gotRaw, err := Engine{}.Probe(context.Background(), path)
			require.NoError(t, err)

			// Codec profile names ("Main 10", "LC") are ffprobe's alone;
			// nothing the standard decides reads them.
			want.VideoProfile = ""
			for i := range want.Audio {
				want.Audio[i].Profile = ""
			}
			assert.Equal(t, want, got)
			assert.Equal(t, wantRaw.MasteringDisplay, gotRaw.MasteringDisplay)
			assert.Equal(t, wantRaw.ContentLight, gotRaw.ContentLight)
			assert.Equal(t, wantRaw.ColorTransfer, gotRaw.ColorTransfer)

			hashOf := func(mi *commonv1.MediaInfo, raw *mediainfo.Raw) string {
				info, err := transcode.FromProbe(mi, raw)
				require.NoError(t, err)
				return standard.Plan(info, standard.Profile{Name: "p", Hash: "h", Quality: 24}, standard.Hardware{Tier: transcode.TierCPUx265}).Hash()
			}
			assert.Equal(t, hashOf(want, wantRaw), hashOf(got, gotRaw), "the same standard plan")
		})
	}
}
