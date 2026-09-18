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
// Field names and endpoint shapes below were verified two ways: directly
// against Bazarr's own provider source, custom_libs/subliminal_patch/
// providers/gestdown.py (fetched from morpheus65535/bazarr on 2026-09-18),
// since docs/research/subtitles.md §4.2/§13.6 does not capture Gestdown's
// response field names; and, for the show-lookup step (fix round 1, item
// 6), against a live call to https://api.gestdown.info/shows/external/tvdb/
// 81189 on 2026-09-18 (Breaking Bad's real TVDB id), whose response shape
// the testdata/subtitles/gestdown/shows.json fixture reproduces verbatim.
// One remaining, disclosed simplification: the per-language Addic7ed code
// conversion (PatchedAddic7edConverter) is skipped — the plain BCP-47
// language tag is sent as-is, which matches Addic7ed's own code for English
// and the languages this package's own tests exercise, but is not verified
// for the full language table.
package gestdown

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
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
type Provider struct {
	cfg Config

	mu      sync.Mutex
	showIDs map[string]string // TVDB id -> Gestdown's own internal show id, cached for this Provider's lifetime
}

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
func (p *Provider) HIVerifiable() bool { return true } // research note §4.1's hearing_impaired_verifiable list explicitly includes gestdown, confirmed against gestdown.py's own `hearing_impaired_verifiable = True` class attribute — the brief's own Step 42 draft said false here, which contradicts the note it cites; corrected, see the task report.
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
// TVDB ids are resolved to Gestdown's own internal show id first (see
// resolveShowID) — a real two-step flow, verified live, not the earlier
// draft's simplification of using the TVDB id directly as a path segment.
func (p *Provider) Search(ctx context.Context, q subtitles.Query) ([]subtitles.Candidate, error) {
	if q.Kind != common.MediaKindEpisode {
		return nil, nil
	}
	ctx, span := tracing.Start(ctx, "subtitles.gestdown.search")
	defer span.End()

	showID, err := p.resolveShowID(ctx, q.IDs["tvdb"])
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	lang := "en"
	if len(q.Languages) > 0 {
		if l, _, _, err := subtitles.ParseLangKey(q.Languages[0]); err == nil {
			lang = l
		}
	}
	url := fmt.Sprintf("%s/subtitles/get/%s/%d/%d/%s", p.cfg.Endpoint, showID, q.Season, q.Episode, lang)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, fmt.Errorf("subtitles: gestdown search: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		err := classifyNonOKStatus(p.Name(), resp)
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

// showsResponse mirrors /shows/external/tvdb/{id}'s real response shape,
// verified live against api.gestdown.info (see the package doc comment):
// {"shows":[{"id": "<uuid>", "tvDbId": <int>, ...}]}. Only the two fields
// this package needs are decoded.
type showsResponse struct {
	Shows []struct {
		ID     string `json:"id"`
		TVDbID int    `json:"tvDbId"`
	} `json:"shows"`
}

// resolveShowID resolves tvdbID to Gestdown's own internal show id via GET
// /shows/external/tvdb/{tvdbID}, caching the result for the Provider's
// lifetime (a TVDB id's mapping to a Gestdown show id is effectively
// permanent). A 404 — verified live: Gestdown returns one for a TVDB id it
// has never indexed, with a bare JSON string body, not an object — maps to
// a subtitles.KindNotFound ProviderError, distinct from "the show exists
// but has no subtitles for this language/episode".
func (p *Provider) resolveShowID(ctx context.Context, tvdbID string) (string, error) {
	p.mu.Lock()
	if id, ok := p.showIDs[tvdbID]; ok {
		p.mu.Unlock()
		return id, nil
	}
	p.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.Endpoint+"/shows/external/tvdb/"+tvdbID, nil)
	if err != nil {
		return "", err
	}
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("subtitles: gestdown show lookup: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return "", &subtitles.ProviderError{Provider: p.Name(), Kind: subtitles.KindNotFound, Err: fmt.Errorf("tvdb id %s: no matching show", tvdbID)}
	}
	if resp.StatusCode != http.StatusOK {
		return "", classifyNonOKStatus(p.Name(), resp)
	}

	var sr showsResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return "", fmt.Errorf("subtitles: gestdown show lookup: decode: %w", err)
	}
	if len(sr.Shows) == 0 {
		return "", &subtitles.ProviderError{Provider: p.Name(), Kind: subtitles.KindNotFound, Err: fmt.Errorf("tvdb id %s: no matching show", tvdbID)}
	}

	id := sr.Shows[0].ID
	p.mu.Lock()
	if p.showIDs == nil {
		p.showIDs = map[string]string{}
	}
	p.showIDs[tvdbID] = id
	p.mu.Unlock()
	return id, nil
}

// classifyNonOKStatus maps a non-200 Gestdown response — from either the
// show lookup or the subtitle search — to a subtitles.ProviderError: 423
// ("refreshing, retry in 30s", research note §4.2, gestdown.py's
// _retry_on_423) or a generic ServiceUnavailable otherwise. Callers needing
// a distinct 404 mapping (resolveShowID) check for it before falling back
// to this.
func classifyNonOKStatus(provider string, resp *http.Response) error {
	if resp.StatusCode == http.StatusLocked {
		return &subtitles.ProviderError{Provider: provider, Kind: subtitles.KindServiceUnavailable, RetryAfter: 30 * time.Second}
	}
	return &subtitles.ProviderError{Provider: provider, Kind: subtitles.KindServiceUnavailable, Err: fmt.Errorf("http %d", resp.StatusCode)}
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
	defer func() { _ = resp.Body.Close() }()
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
