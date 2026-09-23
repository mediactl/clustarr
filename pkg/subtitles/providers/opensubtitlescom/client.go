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
	"errors"
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

// maxSubtitleBytes bounds how much of a subtitle download Provider will
// buffer into memory, mirroring the 8 MiB cap pkg/torznab and
// pkg/cardigann already apply to indexer responses.
const maxSubtitleBytes = 8 << 20 // 8 MiB

// maxErrorBodyBytes bounds how much of a non-2xx body is read for the
// error message. It is deliberately far smaller than maxSubtitleBytes and
// deliberately truncates instead of failing: the body is decoration on a
// status code that already carries the meaning (and Phase F puts the
// message in a CRD status condition), so an oversized body must never mask
// the status it arrived with.
const maxErrorBodyBytes = 4 << 10 // 4 KiB

// maxJSONBytes bounds a JSON API response -- /login, a /subtitles search
// page, the /download link -- which is read whole before it is decoded. A
// real search page is tens of kilobytes; the cap only has to stop a
// misbehaving (or hostile) endpoint from exhausting the worker's memory,
// the same job maxSubtitleBytes does for the subtitle file itself.
const maxJSONBytes = 4 << 20 // 4 MiB

// ErrResponseTooLarge is returned when a response body exceeds its cap: a
// subtitle download past maxSubtitleBytes, or a JSON API response past
// maxJSONBytes.
var ErrResponseTooLarge = errors.New("subtitles: opensubtitlescom: response body exceeds size limit")

// decodeJSON reads body through maxJSONBytes and decodes it into v. It
// reads one byte past the cap, so a body exactly at the cap is accepted
// and anything larger is ErrResponseTooLarge -- never truncated JSON handed
// to the decoder, and never more than maxJSONBytes+1 bytes buffered.
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

// Config configures a Provider.
type Config struct {
	APIKey, Username, Password, UserAgent string
	Endpoint                              string        // default "https://api.opensubtitles.com/api/v1"
	HTTPClient                            *http.Client  // default http.DefaultClient
	Limiter                               *rate.Limiter // default nil — no client-side pacing; see New's doc comment (ruling R3)

	// TokenCache, if set, shares this account's login token with every
	// other Provider (and every other replica) wired to the same cache. A
	// Provider adopts a still-fresh token from it before logging in, and
	// stores every token it obtains. Nil keeps the token in this Provider
	// alone -- the behaviour before the cache existed. See [TokenCache].
	TokenCache TokenCache
}

// Provider implements subtitles.Provider against OpenSubtitles.com.
type Provider struct {
	cfg Config

	mu        sync.Mutex
	token     string
	baseURL   string
	expiresAt time.Time // token's expiry: its JWT exp claim, else login time + tokenLifetime
}

// New builds a Provider from cfg, applying defaults for any zero field.
//
// It does NOT default cfg.Limiter. The caller owns rate limiting
// (CLAUDE.md's "Conventions across pkg/", which names this exact client as
// one that follows it): a library defaulting a limiter on means every
// Provider gets its own private allowance, so N fetch workers sharing one
// OpenSubtitles.com account would collectively exceed the account's real
// rate by a factor of N. captionarr's fetch worker (F-5) is expected to pass
// [Config.Limiter] wired to the shared clustarr-provider-throttle KV token
// bucket ([throttle.Acquire]) instead, or -- for a single-process caller
// that genuinely wants a local-only limiter -- its own *rate.Limiter. A nil
// Limiter means "no client-side pacing at all", exercised in every existing
// test in this package.
func New(cfg Config) *Provider {
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultEndpoint
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &Provider{cfg: cfg, baseURL: cfg.Endpoint}
}

// wait blocks on cfg.Limiter if one was supplied, or returns immediately if
// not -- see New's doc comment on why no default is applied here.
func (p *Provider) wait(ctx context.Context) error {
	if p.cfg.Limiter == nil {
		return nil
	}
	return p.cfg.Limiter.Wait(ctx)
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

// EnsureLoggedIn makes sure the Provider holds a token fresh enough to use
// (see tokenFresh): the one it already has, else a fresh one from
// Config.TokenCache, else a new login.
func (p *Provider) EnsureLoggedIn(ctx context.Context) error { return p.ensureToken(ctx, "") }

// ensureToken is EnsureLoggedIn with one addition: rejected, when non-empty,
// is a token the API has just refused with 401, which is reused from
// neither this Provider nor the cache. Two requests refused with the same
// token therefore cost one login: the first replaces p.token, and the
// second finds p.token no longer equal to what it was refused with and
// uses the replacement.
func (p *Provider) ensureToken(ctx context.Context, rejected string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if rejected != "" && p.token == rejected {
		p.token, p.expiresAt = "", time.Time{}
	}
	if p.token != "" && tokenFresh(p.token, p.expiresAt, now) {
		return nil
	}
	if p.cfg.TokenCache != nil {
		tok, exp, err := p.cfg.TokenCache.LoadToken(ctx)
		switch {
		case err != nil:
			// The cache is an optimisation: an unreadable one costs a
			// login, not the search.
			logging.FromContext(ctx).Warn("opensubtitlescom: token cache unreadable; logging in", "err", err)
		case tok != "" && tok != rejected && tokenFresh(tok, exp, now):
			p.token, p.expiresAt = tok, exp
			return nil
		}
	}
	return p.loginLocked(ctx, now)
}

// loginLocked performs POST /login and stores the token in p and in
// Config.TokenCache. Callers hold p.mu, so concurrent callers queue behind
// one login rather than each spending the account's login allowance
// (1 req/s, research note §4.3).
func (p *Provider) loginLocked(ctx context.Context, now time.Time) error {
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
	p.token = "" // a login request carries no bearer token
	p.setCommonHeadersLocked(req)

	if err := p.wait(ctx); err != nil {
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
	if err := decodeJSON(resp.Body, &lr); err != nil {
		return fmt.Errorf("subtitles: opensubtitlescom login: decode: %w", err)
	}
	if lr.Token == "" {
		// Bazarr: "Cannot get token from provider login response".
		err := &subtitles.ProviderError{Provider: p.Name(), Kind: subtitles.KindParse, Err: errors.New("login response carries no token")}
		tracing.RecordError(span, err)
		return err
	}
	p.token = lr.Token
	p.expiresAt = tokenExpiry(lr.Token, now)
	logging.FromContext(ctx).Debug("opensubtitlescom login ok", "vip", lr.User.VIP, "expiresAt", p.expiresAt)

	if p.cfg.TokenCache != nil {
		if err := p.cfg.TokenCache.StoreToken(ctx, p.token, p.expiresAt); err != nil {
			// This Provider has its token; the other replicas will log in
			// themselves. Worth a warning, not a failed search.
			logging.FromContext(ctx).Warn("opensubtitlescom: could not share the login token", "err", err)
		}
	}
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
// EnsureLoggedIn has already returned. It returns the bearer token it set,
// so a 401 can name the token that was refused (see doAuthed).
func (p *Provider) setAuthHeaders(req *http.Request) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.setCommonHeadersLocked(req)
	return p.token
}

// doAuthed sends the request newReq builds, with the account's token. On a
// 401 it logs in again -- the token was revoked or expired early -- and
// retries once with a request newReq builds afresh (a POST body cannot be
// replayed); a second 401 is returned as the KindAuth error it maps to.
// That is Bazarr's checked(): "401: reset token, re-login once".
func (p *Provider) doAuthed(ctx context.Context, newReq func() (*http.Request, error)) (*http.Response, error) {
	if err := p.EnsureLoggedIn(ctx); err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		req, err := newReq()
		if err != nil {
			return nil, err
		}
		used := p.setAuthHeaders(req)
		if err := p.wait(ctx); err != nil {
			return nil, err
		}
		resp, err := p.cfg.HTTPClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusUnauthorized || attempt > 0 {
			return resp, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		_ = resp.Body.Close()
		logging.FromContext(ctx).Debug("opensubtitlescom: token refused; logging in again")
		if err := p.ensureToken(ctx, used); err != nil {
			return nil, err
		}
	}
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
	// Read one byte past the cap so an over-long body can be reported as
	// truncated; the extra byte is dropped from the message either way.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes+1))
	msg := string(body)
	if len(body) > maxErrorBodyBytes {
		body = body[:maxErrorBodyBytes]
		msg = string(body) + " ... (truncated)"
	}
	pe := &subtitles.ProviderError{Provider: provider, Err: fmt.Errorf("http %d: %s", resp.StatusCode, msg)}

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
