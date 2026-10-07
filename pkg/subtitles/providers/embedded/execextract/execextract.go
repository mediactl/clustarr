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

// Package execextract is the transitional embedded-subtitle extractor that runs
// the ffmpeg executable (spec §4.2.5). It runs
// `ffmpeg -y -i P -map 0:N -c:s srt -f srt pipe:1`, exactly what
// pkg/subtitles/providers/embedded ran before it took an ExtractFunc. The R2
// step replaces it with embedded/native (ffgo) and deletes it.
package execextract

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"

	"github.com/mediactl/clustarr/pkg/subtitles/providers/embedded"
)

// New returns an embedded.ExtractFunc that runs ffmpeg: the binary on PATH
// when ffmpeg is empty.
func New(ffmpeg string) embedded.ExtractFunc {
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	return func(ctx context.Context, path string, stream int) ([]byte, error) {
		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, ffmpeg, "-y", "-i", path,
			"-map", "0:"+strconv.Itoa(stream), "-c:s", "srt", "-f", "srt", "pipe:1")
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("ffmpeg: %w: %s", err, stderr.String())
		}
		return stdout.Bytes(), nil
	}
}
