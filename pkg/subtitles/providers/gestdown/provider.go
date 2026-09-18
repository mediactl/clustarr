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

// Package gestdown implements subtitles.Provider against the Gestdown
// (api.gestdown.info) TV-subtitle API.
//
// Field names and endpoint shapes below were verified directly against
// Bazarr's own provider source, custom_libs/subliminal_patch/providers/
// gestdown.py (fetched from morpheus65535/bazarr on 2026-09-18), since
// docs/research/subtitles.md §4.2/§13.6 does not capture Gestdown's
// response field names — see the task report's "adapted to real pkg/release"
// / Gestdown section for the exact source excerpt this was checked against.
// Two deliberate simplifications versus the real gestdown.py, both called
// out where they apply below: (1) Query.IDs["tvdb"] is used directly as
// Gestdown's own internal show id, skipping the real client's
// /shows/external/tvdb/{id} resolution step (Bazarr does this because a
// single TVDB id can map to more than one Gestdown "show" entry — e.g.
// regional variants — this package always searches exactly one); (2) the
// per-language Addic7ed code conversion (PatchedAddic7edConverter) is
// skipped — the plain BCP-47 language tag is sent as-is, which matches
// Addic7ed's own code for English and the languages this package's own
// tests exercise, but is not verified for the full language table.
package gestdown

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

const defaultEndpoint = "https://api.gestdown.info"

// Config configures a Provider.
type Config struct {
	Endpoint   string // default "https://api.gestdown.info"
	HTTPClient *http.Client
}

// Provider implements subtitles.Provider against Gestdown.
type Provider struct{ cfg Config }

// New builds a Provider from cfg, applying defaults for any zero field.
func New(cfg Config) *Provider {
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultEndpoint
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &Provider{cfg: cfg}
}

func (p *Provider) Name() string       { return "gestdown" }
func (p *Provider) HIVerifiable() bool { return false } // not in research note §4.1's hearing_impaired_verifiable list; gestdown.py sets hash_verifiable=False and hearing_impaired_verifiable=True on the *subtitle*, but this package tracks HIVerifiable per-provider, and Gestdown never corroborates a hash match (it has none) — see subtitles.Capabilities.HashVerifiable below.
func (p *Provider) Capabilities() subtitles.Capabilities {
	return subtitles.Capabilities{Episodes: true} // TV only, research note §4.2
}

// searchResponse mirrors matchingSubtitles' real field names, verified
// against gestdown.py's GestdownSubtitle.__init__: hearingImpaired,
// downloadUri, subtitleId, version, completed.
type searchResponse struct {
	MatchingSubtitles []struct {
		SubtitleID      string `json:"subtitleId"`
		Version         string `json:"version"`
		DownloadURI     string `json:"downloadUri"`
		Completed       bool   `json:"completed"`
		HearingImpaired bool   `json:"hearingImpaired"`
	} `json:"matchingSubtitles"`
}

// Search implements subtitles.Provider.Search. Gestdown is TV-only: a
// non-episode Query returns no candidates without touching the network.
func (p *Provider) Search(ctx context.Context, q subtitles.Query) ([]subtitles.Candidate, error) {
	if q.Kind != common.MediaKindEpisode {
		return nil, nil
	}
	ctx, span := tracing.Start(ctx, "subtitles.gestdown.search")
	defer span.End()

	lang := "en"
	if len(q.Languages) > 0 {
		if l, _, _, err := subtitles.ParseLangKey(q.Languages[0]); err == nil {
			lang = l
		}
	}
	url := fmt.Sprintf("%s/subtitles/get/%s/%d/%d/%s", p.cfg.Endpoint, q.IDs["tvdb"], q.Season, q.Episode, lang)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, fmt.Errorf("subtitles: gestdown search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusLocked { // 423 "refreshing, retry in 30s" — research note §4.2, gestdown.py's _retry_on_423
		err := &subtitles.ProviderError{Provider: p.Name(), Kind: subtitles.KindServiceUnavailable, RetryAfter: 30 * time.Second}
		tracing.RecordError(span, err)
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		err := &subtitles.ProviderError{Provider: p.Name(), Kind: subtitles.KindServiceUnavailable, Err: fmt.Errorf("http %d", resp.StatusCode)}
		tracing.RecordError(span, err)
		return nil, err
	}

	var sr searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("subtitles: gestdown search: decode: %w", err)
	}
	out := make([]subtitles.Candidate, 0, len(sr.MatchingSubtitles))
	for _, m := range sr.MatchingSubtitles {
		if !m.Completed { // gestdown.py: "if not subtitle_dict['completed']: continue"
			continue
		}
		out = append(out, subtitles.Candidate{
			Provider: p.Name(), ID: m.SubtitleID, FetchID: m.DownloadURI, Language: lang,
			HI: m.HearingImpaired, ReleaseInfo: releaseInfoFromVersion(m.Version),
		})
	}
	return out, nil
}

// releaseInfoFromVersion mirrors gestdown.py's release_info construction:
// "version" is a comma-separated list of release tags, split, trimmed and
// newline-joined.
func releaseInfoFromVersion(version string) string {
	parts := strings.Split(version, ",")
	for i, v := range parts {
		parts[i] = strings.TrimSpace(v)
	}
	return strings.Join(parts, "\n")
}

// Download implements subtitles.Provider.Download. c.FetchID is the raw
// downloadUri Search returned (a path relative to the endpoint) — verified
// against gestdown.py: page_link = _BASE_URL + data["downloadUri"], fetched
// directly, not reconstructed from an id.
func (p *Provider) Download(ctx context.Context, c subtitles.Candidate) ([]byte, string, error) {
	ctx, span := tracing.Start(ctx, "subtitles.gestdown.download")
	defer span.End()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.Endpoint+c.FetchID, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, "", fmt.Errorf("subtitles: gestdown download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err := &subtitles.ProviderError{Provider: p.Name(), Kind: subtitles.KindServiceUnavailable, Err: fmt.Errorf("http %d", resp.StatusCode)}
		tracing.RecordError(span, err)
		return nil, "", err
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("subtitles: gestdown download: read body: %w", err)
	}
	return raw, c.ReleaseInfo + ".srt", nil
}
