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

// Package opensubtitlescom implements subtitles.Provider against the
// OpenSubtitles.com REST v1 API (docs/research/subtitles.md §4.3).
package opensubtitlescom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

const defaultEndpoint = "https://api.opensubtitles.com/api/v1"

// Config configures a Provider.
type Config struct {
	APIKey, Username, Password, UserAgent string
	Endpoint                              string        // default "https://api.opensubtitles.com/api/v1"
	HTTPClient                            *http.Client  // default http.DefaultClient
	Limiter                               *rate.Limiter // default rate.NewLimiter(5, 5) — 5 req/s per §4.3
}

// Provider implements subtitles.Provider against OpenSubtitles.com.
type Provider struct {
	cfg Config

	mu      sync.Mutex
	token   string
	baseURL string
	tokenAt time.Time
}

// New builds a Provider from cfg, applying defaults for any zero field.
func New(cfg Config) *Provider {
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultEndpoint
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.Limiter == nil {
		cfg.Limiter = rate.NewLimiter(5, 5) // 5 req/s per IP, research note §4.3
	}
	return &Provider{cfg: cfg, baseURL: cfg.Endpoint}
}

func (p *Provider) Name() string       { return "opensubtitlescom" }
func (p *Provider) HIVerifiable() bool { return true } // research note §4.1
func (p *Provider) Capabilities() subtitles.Capabilities {
	return subtitles.Capabilities{
		Movies: true, Episodes: true, ForcedSearch: true, HashSearch: true, HashVerifiable: true,
		NeedsSecrets: []string{"apiKey", "username", "password"},
	}
}

type loginResponse struct {
	Token   string `json:"token"`
	BaseURL string `json:"base_url"`
	User    struct {
		VIP bool `json:"vip"`
	} `json:"user"`
}

// EnsureLoggedIn logs in if there is no cached token, or the cached one is
// older than 12h (Bazarr's TOKEN_EXPIRATION_TIME, half the real 24h JWT
// life — research note §4.3).
func (p *Provider) EnsureLoggedIn(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token != "" && time.Since(p.tokenAt) < 12*time.Hour {
		return nil
	}

	ctx, span := tracing.Start(ctx, "subtitles.opensubtitlescom.login")
	defer span.End()

	body, err := json.Marshal(map[string]string{"username": p.cfg.Username, "password": p.cfg.Password})
	if err != nil {
		return fmt.Errorf("subtitles: opensubtitlescom login: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.Endpoint+"/login", bytes.NewReader(body))
	if err != nil {
		return err
	}
	p.setCommonHeadersLocked(req)

	if err := p.cfg.Limiter.Wait(ctx); err != nil {
		return err
	}
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		tracing.RecordError(span, err)
		return fmt.Errorf("subtitles: opensubtitlescom login: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		err := statusToProviderError(p.Name(), resp)
		tracing.RecordError(span, err)
		return err
	}

	var lr loginResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return fmt.Errorf("subtitles: opensubtitlescom login: decode: %w", err)
	}
	p.token = lr.Token
	p.tokenAt = time.Now()
	logging.FromContext(ctx).Debug("opensubtitlescom login ok", "vip", lr.User.VIP)
	return nil
}

// setCommonHeadersLocked sets the headers every OpenSubtitles.com request
// needs. Callers must hold p.mu (or be certain no concurrent token refresh
// is in flight) since it reads p.token.
func (p *Provider) setCommonHeadersLocked(req *http.Request) {
	req.Header.Set("Api-Key", p.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	ua := p.cfg.UserAgent
	if ua == "" {
		ua = "clustarr v0"
	}
	req.Header.Set("User-Agent", ua)
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
}

// setAuthHeaders sets the same headers as setCommonHeadersLocked but takes
// its own lock — used by Search/Download, which call it after
// EnsureLoggedIn has already returned (so no login is in flight).
func (p *Provider) setAuthHeaders(req *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.setCommonHeadersLocked(req)
}

// quotaBody is the OpenSubtitles.com 406 (download limit exceeded) body
// shape (research note §4.3): {"message", "remaining", "reset_time",
// "reset_time_utc"}. Best-effort: a body that isn't this shape (a
// different status's plain {"message"} body, for instance) just leaves
// Remaining/ResetAt at their zero values rather than failing the whole
// error-mapping path.
type quotaBody struct {
	Remaining    int    `json:"remaining"`
	ResetTimeUTC string `json:"reset_time_utc"`
}

// statusToProviderError maps an OpenSubtitles HTTP response to
// subtitles.ProviderError per research note §4.3's status table.
func statusToProviderError(provider string, resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	pe := &subtitles.ProviderError{Provider: provider, Err: fmt.Errorf("http %d: %s", resp.StatusCode, string(body))}

	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil {
			pe.RetryAfter = time.Duration(secs) * time.Second
		}
	}

	if resp.StatusCode == http.StatusNotAcceptable { // 406: remaining/reset_time_utc are only documented for this status
		var qb quotaBody
		if err := json.Unmarshal(body, &qb); err == nil {
			pe.Remaining = qb.Remaining
			if t, err := time.Parse(time.RFC3339, qb.ResetTimeUTC); err == nil {
				pe.ResetAt = t
			}
		}
	}

	switch resp.StatusCode {
	case http.StatusBadRequest:
		pe.Kind = subtitles.KindConfig
	case http.StatusUnauthorized, http.StatusForbidden:
		pe.Kind = subtitles.KindAuth
	case http.StatusNotAcceptable: // 406
		pe.Kind = subtitles.KindDownloadLimitExceeded
	case http.StatusTooManyRequests: // 429
		pe.Kind = subtitles.KindTooManyRequests
	case http.StatusBadGateway: // 502
		pe.Kind = subtitles.KindAPIThrottled
	case http.StatusGone: // 410, download link expired
		pe.Kind = subtitles.KindParse
	default:
		pe.Kind = subtitles.KindServiceUnavailable
	}
	return pe
}
