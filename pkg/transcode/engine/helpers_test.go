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

package engine

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/obinnaokechukwu/ffgo"
)

// ffmpeg9OrSkip skips unless FFmpeg 9's libraries and the ffmpeg/ffprobe
// executables (which make and inspect the clips) are here.
func ffmpeg9OrSkip(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("no %s to make or inspect test clips", bin)
		}
	}
	if err := ffgo.Init(); err != nil {
		t.Skipf("no FFmpeg libraries: %v", err)
	}
	if _, avc, _ := ffgo.Version(); avc>>16 != 63 {
		t.Skip("not FFmpeg 9")
	}
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// everythingClip: H.264 video; English AC-3 5.1 (default); French AAC
// stereo "Commentary" (comment); English SRT (forced); a font; two
// chapters; container title "Clip". seconds long.
func everythingClip(t *testing.T, seconds int) string {
	t.Helper()
	ffmpeg9OrSkip(t)
	dir := t.TempDir()
	srt := filepath.Join(dir, "sub.srt")
	font := filepath.Join(dir, "font.ttf")
	meta := filepath.Join(dir, "chapters.ffmeta")
	half := seconds * 500
	for path, body := range map[string]string{
		srt:  "1\n00:00:00,500 --> 00:00:01,500\nHello\n",
		font: "not really a font",
		meta: ";FFMETADATA1\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nEND=" + strconv.Itoa(half) + "\ntitle=One\n[CHAPTER]\nTIMEBASE=1/1000\nSTART=" + strconv.Itoa(half) + "\nEND=" + strconv.Itoa(2*half) + "\ntitle=Two\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, "everything.mkv")
	d := strconv.Itoa(seconds)
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration="+d,
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration="+d+",aformat=channel_layouts=5.1",
		"-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000:duration="+d,
		"-i", srt, "-i", meta,
		"-map", "0", "-map", "1", "-map", "2", "-map", "3", "-map_chapters", "4",
		"-c:v", "libx264", "-preset", "veryfast", "-bf", "2", "-c:a:0", "ac3", "-c:a:1", "aac", "-c:s", "srt",
		"-attach", font, "-metadata:s:t", "mimetype=application/x-truetype-font",
		"-metadata:s:a:0", "language=eng", "-metadata:s:a:1", "language=fre", "-metadata:s:a:1", "title=Commentary",
		"-metadata:s:s:0", "language=eng",
		"-disposition:a:0", "default", "-disposition:a:1", "comment", "-disposition:s:0", "forced",
		"-metadata", "title=Clip", out)
	return out
}

type probed struct {
	Streams []struct {
		CodecName   string            `json:"codec_name"`
		CodecType   string            `json:"codec_type"`
		Profile     string            `json:"profile"`
		PixFmt      string            `json:"pix_fmt"`
		Channels    int               `json:"channels"`
		Layout      string            `json:"channel_layout"`
		StartTime   string            `json:"start_time"`
		ColorTrc    string            `json:"color_transfer"`
		Tags        map[string]string `json:"tags"`
		Disposition map[string]int    `json:"disposition"`
	} `json:"streams"`
	Chapters []struct {
		Tags map[string]string `json:"tags"`
	} `json:"chapters"`
	Format struct {
		Duration string            `json:"duration"`
		Tags     map[string]string `json:"tags"`
	} `json:"format"`
}

func ffprobeJSON(t *testing.T, path string) probed {
	t.Helper()
	var p probed
	out := run(t, "ffprobe", "-v", "error", "-show_streams", "-show_chapters", "-show_format", "-of", "json", path)
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatalf("ffprobe json: %v\n%s", err, out)
	}
	return p
}

func (p probed) seconds(t *testing.T) float64 {
	t.Helper()
	d, err := strconv.ParseFloat(p.Format.Duration, 64)
	if err != nil {
		t.Fatalf("duration %q", p.Format.Duration)
	}
	return d
}
