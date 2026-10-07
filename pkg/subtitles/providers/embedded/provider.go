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
// embedded subtitle tracks. Search reads an already-probed common.MediaInfo;
// this package never probes. Download hands one text track to the process's
// ExtractFunc, which returns it as SRT. The package runs no program: the
// manager links it for Search alone (spec §7.3.1), and only the fetch worker's
// process sets an extractor: embedded/native, FFmpeg in-process through ffgo
// (spec 2026-10-06 §7.3.1).
package embedded

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// ExtractFunc returns stream (the container stream index) of the file at
// path, as SRT.
type ExtractFunc func(ctx context.Context, path string, stream int) ([]byte, error)

// ErrNoExtractor is Download's error in a process that set no Config.Extract.
var ErrNoExtractor = errors.New("subtitles: embedded: no extractor in this process")

// textCodecs are the codecs an extractor can write as text subtitles
// (research note §3.1, §4.6): subrip, ass, ssa, webvtt, mov_text.
var textCodecs = map[string]bool{"subrip": true, "ass": true, "ssa": true, "webvtt": true, "mov_text": true}

// IsTextCodec reports whether codec is a text subtitle codec an extractor can
// write as SRT.
func IsTextCodec(codec string) bool { return textCodecs[codec] }

// Config configures a Provider.
type Config struct {
	Path           string           // media file path the extractor reads
	Info           common.MediaInfo // already-probed; this package never probes
	IgnoreASS      bool             // mirrors SubtitleProfileSpec.Embedded.IgnoreASS
	SkipCommentary bool             // mirrors SubtitleProfileSpec.Embedded.SkipCommentary
	// Extract pulls one stream out of Path as SRT. Nil: Download returns
	// ErrNoExtractor.
	Extract ExtractFunc
}

// Provider implements subtitles.Provider over a file's own embedded
// subtitle tracks.
type Provider struct{ cfg Config }

// New builds a Provider from cfg.
func New(cfg Config) *Provider { return &Provider{cfg: cfg} }

func (p *Provider) Name() string       { return "embedded" }
func (p *Provider) HIVerifiable() bool { return true } // research note §4.1, §4.6
// Capabilities reports HashVerifiable false. The "hash" match Search sets is
// not a moviehash a remote database happened to agree with -- the candidate
// IS a stream of the file being searched for -- so there is nothing to
// corroborate, and Bazarr's embedded provider does not mark its subtitles
// hash-verifiable either. Claiming true made subtitles.CandidateMatches
// demand video_codec and source matches no embedded candidate carries, drop
// the hash, and score every embedded track at 0 or 1: nothing embedded could
// ever reach a profile's minimum score.
func (p *Provider) Capabilities() subtitles.Capabilities {
	return subtitles.Capabilities{Movies: true, Episodes: true, ForcedSearch: true, HashVerifiable: false}
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
		if s.Bitmap || !IsTextCodec(s.Codec) {
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

// Download extracts the subtitle stream c.FetchID names (the container
// stream index, as a decimal string) through Config.Extract.
func (p *Provider) Download(ctx context.Context, c subtitles.Candidate) ([]byte, string, error) {
	ctx, span := tracing.Start(ctx, "subtitles.embedded.download")
	defer span.End()

	idx, err := strconv.Atoi(c.FetchID)
	if err != nil {
		return nil, "", fmt.Errorf("subtitles: embedded: invalid stream index %q: %w", c.FetchID, err)
	}
	if p.cfg.Extract == nil {
		tracing.RecordError(span, ErrNoExtractor)
		return nil, "", ErrNoExtractor
	}
	raw, err := p.cfg.Extract(ctx, p.cfg.Path, idx)
	if err != nil {
		wrapped := fmt.Errorf("subtitles: embedded extract (stream %s): %w", c.FetchID, err)
		tracing.RecordError(span, wrapped)
		return nil, "", wrapped
	}
	return raw, "stream-" + c.FetchID + ".srt", nil
}
