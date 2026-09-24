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
// the test/data/subtitles/gestdown/shows.json fixture reproduces verbatim.
// One remaining, disclosed simplification: the per-language Addic7ed code
// conversion (PatchedAddic7edConverter) is skipped — the plain BCP-47
// language tag is sent as-is, which matches Addic7ed's own code for English
// and the languages this package's own tests exercise, but is not verified
// for the full language table.
package gestdown

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

const defaultEndpoint = "https://api.gestdown.info"

// Config configures a Provider.
type Config struct {
	Endpoint   string // default "https://api.gestdown.info"
	HTTPClient *http.Client

	// Limiter, if set, paces every outbound request (show lookup, subtitle
	// search and download alike) on the caller's own budget. It defaults to
	// nil -- no client-side pacing -- rather than to a limiter this package
	// invents itself: CLAUDE.md's "the caller owns rate limiting" rule,
	// ruling R3. A library defaulting one on would give every Provider
	// instance a private allowance, so N fetch workers sharing this process
	// would collectively out-pace whatever budget the caller intended.
	// captionarr's fetch worker (F-5) is expected to wire this to the shared
	// clustarr-provider-throttle KV token bucket the same way it wires
	// opensubtitlescom.Config.Limiter.
	Limiter *rate.Limiter
}

// Provider implements subtitles.Provider against Gestdown.
type Provider struct {
	cfg Config

	mu      sync.Mutex
	showIDs map[string]string // TVDB id -> Gestdown's own internal show id, cached for this Provider's lifetime
	// inflight is the show lookup currently running for each TVDB id, so
	// concurrent searches for episodes of one show on a cold cache share one
	// GET /shows/external/tvdb/{id} instead of each spending a request of
	// the caller's shared budget on the same answer (see resolveShowID).
	inflight map[string]*showLookup
}

// showLookup is one in-flight show-id resolution. done is closed once id
// and err are final; every caller that joined the lookup reads them only
// after that.
type showLookup struct {
	done chan struct{}
	id   string
	err  error
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

// wait blocks on cfg.Limiter if one was supplied, or returns immediately if
// not -- see Config.Limiter's doc comment on why no default is applied here.
func (p *Provider) wait(ctx context.Context) error {
	if p.cfg.Limiter == nil {
		return nil
	}
	return p.cfg.Limiter.Wait(ctx)
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
	if err := p.wait(ctx); err != nil {
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
	if err := decodeJSON(resp.Body, &sr); err != nil {
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

// resolveShowID resolves tvdbID to Gestdown's own internal show id,
// caching the result for the Provider's lifetime (a TVDB id's mapping to a
// Gestdown show id is effectively permanent).
//
// Lookups are single-flight per TVDB id: while one caller's GET
// /shows/external/tvdb/{id} is in flight, every other caller asking for the
// same id waits for that request's answer rather than issuing its own. A
// season's worth of episodes searched in parallel on a cold cache is
// otherwise one identical lookup per episode, each spending a request of
// the caller's shared rate budget. Only a success is cached; a failure is
// handed to the callers that waited on it and the next caller tries again.
//
// A waiter's own ctx bounds its wait. If the lookup it joined failed only
// because the leading caller's context ended, and the waiter's own context
// is still live, the waiter retries rather than inheriting a cancellation
// that was never its own.
func (p *Provider) resolveShowID(ctx context.Context, tvdbID string) (string, error) {
	for {
		p.mu.Lock()
		if id, ok := p.showIDs[tvdbID]; ok {
			p.mu.Unlock()
			return id, nil
		}
		if l, ok := p.inflight[tvdbID]; ok {
			p.mu.Unlock()
			select {
			case <-l.done:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			if isContextErr(l.err) && ctx.Err() == nil {
				continue // the leader's context ended, not ours: try again
			}
			return l.id, l.err
		}
		l := &showLookup{done: make(chan struct{})}
		if p.inflight == nil {
			p.inflight = map[string]*showLookup{}
		}
		p.inflight[tvdbID] = l
		p.mu.Unlock()

		l.id, l.err = p.lookupShowID(ctx, tvdbID)

		p.mu.Lock()
		delete(p.inflight, tvdbID)
		if l.err == nil {
			if p.showIDs == nil {
				p.showIDs = map[string]string{}
			}
			p.showIDs[tvdbID] = l.id
		}
		p.mu.Unlock()
		close(l.done)
		return l.id, l.err
	}
}

// isContextErr reports whether err is (or wraps) a context cancellation or
// deadline, however deep the HTTP client buried it.
func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// lookupShowID performs the uncached GET /shows/external/tvdb/{tvdbID}. A
// 404 — verified live: Gestdown returns one for a TVDB id it has never
// indexed, with a bare JSON string body, not an object — maps to a
// subtitles.KindNotFound ProviderError, distinct from "the show exists but
// has no subtitles for this language/episode".
func (p *Provider) lookupShowID(ctx context.Context, tvdbID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.Endpoint+"/shows/external/tvdb/"+tvdbID, nil)
	if err != nil {
		return "", err
	}
	if err := p.wait(ctx); err != nil {
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
	if err := decodeJSON(resp.Body, &sr); err != nil {
		return "", fmt.Errorf("subtitles: gestdown show lookup: decode: %w", err)
	}
	if len(sr.Shows) == 0 {
		return "", &subtitles.ProviderError{Provider: p.Name(), Kind: subtitles.KindNotFound, Err: fmt.Errorf("tvdb id %s: no matching show", tvdbID)}
	}
	return sr.Shows[0].ID, nil
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

// maxSubtitleBytes bounds how much of a subtitle download Provider will
// buffer into memory, mirroring the 8 MiB cap pkg/torznab and
// pkg/cardigann already apply to indexer responses. A real subtitle is a
// few tens of kilobytes; without a bound, one misbehaving (or malicious)
// provider response could exhaust the captionarr worker's memory.
const maxSubtitleBytes = 8 << 20 // 8 MiB

// maxJSONBytes bounds a JSON API response -- the show lookup and the
// subtitle search -- which is read whole before it is decoded. A real
// search response is a few kilobytes; the cap only has to stop a
// misbehaving endpoint from exhausting the worker's memory (CLAUDE.md:
// every HTTP response body is read through a cap).
const maxJSONBytes = 4 << 20 // 4 MiB

// ErrResponseTooLarge is returned when a response body exceeds its cap: a
// subtitle download past maxSubtitleBytes, or a JSON API response past
// maxJSONBytes.
var ErrResponseTooLarge = errors.New("subtitles: gestdown: response body exceeds size limit")

// decodeJSON reads body through maxJSONBytes and decodes it into v. It
// reads one byte past the cap, so a body exactly at the cap is accepted and
// anything larger is ErrResponseTooLarge -- never truncated JSON handed to
// the decoder.
func decodeJSON(body io.Reader, v any) error {
	raw, err := io.ReadAll(io.LimitReader(body, maxJSONBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxJSONBytes {
		return fmt.Errorf("%w: at least %d bytes", ErrResponseTooLarge, len(raw))
	}
	return json.Unmarshal(raw, v)
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
	if err := p.wait(ctx); err != nil {
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
	// Read one byte past the limit so a body exactly at the limit is
	// accepted while anything larger is detected without ever buffering
	// more than maxSubtitleBytes+1 bytes.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxSubtitleBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("subtitles: gestdown download: read body: %w", err)
	}
	if len(raw) > maxSubtitleBytes {
		sizeErr := fmt.Errorf("%w: at least %d bytes", ErrResponseTooLarge, len(raw))
		tracing.RecordError(span, sizeErr)
		return nil, "", sizeErr
	}
	return raw, c.ReleaseInfo + ".srt", nil
}
