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

// Package embedded implements subtitles.Provider over a media file's own
// embedded subtitle tracks: Search reads an already-probed common.MediaInfo
// (this package never calls ffprobe itself — that is pkg/mediainfo's job,
// wave 1), and Download shells out to ffmpeg to extract one text track as
// SRT.
package embedded

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// textCodecs are the codecs ffmpeg can losslessly extract as text
// subtitles (research note §3.1, §4.6): subrip, ass, ssa, webvtt, mov_text.
var textCodecs = map[string]bool{"subrip": true, "ass": true, "ssa": true, "webvtt": true, "mov_text": true}

// Config configures a Provider.
type Config struct {
	FFmpeg         string           // default "ffmpeg"
	Path           string           // media file path ffmpeg reads from
	Info           common.MediaInfo // already-probed; this package never calls ffprobe
	IgnoreASS      bool             // mirrors SubtitleProfileSpec.Embedded.IgnoreASS
	SkipCommentary bool             // mirrors SubtitleProfileSpec.Embedded.SkipCommentary
}

// Provider implements subtitles.Provider over a file's own embedded
// subtitle tracks.
type Provider struct{ cfg Config }

// New builds a Provider from cfg, applying defaults for any zero field.
func New(cfg Config) *Provider {
	if cfg.FFmpeg == "" {
		cfg.FFmpeg = "ffmpeg"
	}
	return &Provider{cfg: cfg}
}

func (p *Provider) Name() string       { return "embedded" }
func (p *Provider) HIVerifiable() bool { return true } // research note §4.1, §4.6
func (p *Provider) Capabilities() subtitles.Capabilities {
	return subtitles.Capabilities{Movies: true, Episodes: true, ForcedSearch: true, HashVerifiable: true}
}

// Search returns one Candidate per eligible text subtitle stream in
// p.cfg.Info. Bitmap codecs (PGS, VobSub) are never text-extractable and are
// always excluded unconditionally — there is deliberately no
// IgnorePGS/IgnoreVobSub knob here, since a "don't ignore bitmap subtitles"
// setting would be meaningless for a provider that can never serve them
// (self-review fix round 1, item 5: those two fields existed in an earlier
// draft, mirroring SubtitleProfileSpec.Embedded verbatim, but were dead —
// nothing in Search ever branched on them). IgnoreASS is the one real,
// consulted flag below (research note §4.6).
func (p *Provider) Search(_ context.Context, _ subtitles.Query) ([]subtitles.Candidate, error) {
	var out []subtitles.Candidate
	for _, s := range p.cfg.Info.Subtitles {
		if s.Bitmap || !textCodecs[s.Codec] {
			continue
		}
		if p.cfg.IgnoreASS && (s.Codec == "ass" || s.Codec == "ssa") {
			continue
		}
		if p.cfg.SkipCommentary && strings.Contains(strings.ToLower(s.Title), "commentary") {
			continue
		}
		out = append(out, subtitles.Candidate{
			Provider: p.Name(), FetchID: strconv.Itoa(int(s.Index)),
			Language: s.Language, HI: s.HearingImpaired, Forced: s.Forced,
			Matches: map[string]bool{subtitles.MatchHash: true, subtitles.MatchHearingImpaired: true},
		})
	}
	return out, nil
}

// Download shells out to ffmpeg to extract the subtitle stream identified
// by c.FetchID (the container stream index, as a decimal string) as SRT.
func (p *Provider) Download(ctx context.Context, c subtitles.Candidate) ([]byte, string, error) {
	ctx, span := tracing.Start(ctx, "subtitles.embedded.download")
	defer span.End()

	if _, err := strconv.Atoi(c.FetchID); err != nil {
		return nil, "", fmt.Errorf("subtitles: embedded: invalid stream index %q: %w", c.FetchID, err)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, p.cfg.FFmpeg, "-y", "-i", p.cfg.Path, "-map", "0:"+c.FetchID, "-c:s", "srt", "-f", "srt", "pipe:1")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		wrapped := fmt.Errorf("subtitles: embedded ffmpeg extract (stream %s): %w: %s", c.FetchID, err, stderr.String())
		tracing.RecordError(span, wrapped)
		return nil, "", wrapped
	}
	return stdout.Bytes(), "stream-" + c.FetchID + ".srt", nil
}
