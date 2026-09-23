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

package subdl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mediactl/clustarr/pkg/subtitles"
)

const (
	// maxJSONBytes bounds a search response, read whole before decoding. A
	// page is at most SUBS_PER_PAGE (30) items of a few hundred bytes each.
	maxJSONBytes = 4 << 20 // 4 MiB
	// maxDownloadBytes bounds a download: an archive, which for a season
	// pack holds a subtitle per episode.
	maxDownloadBytes = 32 << 20 // 32 MiB
	// maxSubtitleBytes bounds one subtitle file, bare or inside an archive,
	// the same 8 MiB cap every other provider here applies.
	maxSubtitleBytes = 8 << 20 // 8 MiB
	// maxErrorBodyBytes bounds how much of a non-200 body is read to learn
	// why: the status code already carries the meaning.
	maxErrorBodyBytes = 4 << 10 // 4 KiB
)

var (
	// ErrResponseTooLarge is returned when a response body exceeds its cap.
	ErrResponseTooLarge = errors.New("subtitles: subdl: response body exceeds size limit")
	// ErrRejected is a 4xx that is about one request -- a title the API
	// refuses to parse, a subtitle that no longer exists -- and says nothing
	// about the provider's health (Bazarr's SubdlRequestRejected). It is
	// deliberately not a subtitles.ProviderError: captionarr throttles the
	// whole provider on one of those, and one dead download link is not a
	// reason to stop using SubDL.
	ErrRejected = errors.New("subtitles: subdl: request rejected")
	// ErrNoSubtitle is returned by Download when the file served holds no
	// subtitle for the candidate. Like ErrRejected it is about the one
	// candidate, not the provider.
	ErrNoSubtitle = errors.New("subtitles: subdl: no subtitle in download")
)

// errNotFoundRoute is ErrRejected for a bare 404. On a search it means the
// search endpoint itself is missing -- an outage -- which Bazarr raises as
// ServiceUnavailable; on a download it is one dead link.
var errNotFoundRoute = fmt.Errorf("%w: resource not found", ErrRejected)

// apiKeyParam matches the api_key query parameter in a URL, for redaction.
var apiKeyParam = regexp.MustCompile(`(api_key=)[^&\s"]+`)

// redact strips the API key from s (Bazarr's _redact): SubDL takes the key
// in the query string, so it is in every request URL, and Go's *url.Error
// quotes the URL in its message -- which captionarr writes into a
// SubtitleRequest's status.
func redact(s string) string { return apiKeyParam.ReplaceAllString(s, "${1}<redacted>") }

// get performs one GET with the caller's limiter and maps the response per
// Bazarr's SubdlProvider.checked. The returned body is open on success.
func (p *Provider) get(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, errors.New(redact(err.Error()))
	}
	req.Header.Set("User-Agent", p.userAgent())
	if err := p.wait(ctx); err != nil {
		return nil, err
	}
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			ue.URL = redact(ue.URL)
		}
		return nil, err
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	return nil, p.statusError(resp)
}

// errorPayload is the JSON body SubDL puts on an error status. Verified
// live on 2026-09-23: a request without a key gets 403
// {"status":false,"statusCode":403,"error":"not_authorized","message":"Not Authorized"}.
type errorPayload struct {
	Error   any    `json:"error"`
	Message string `json:"message"`
}

func (e errorPayload) text() string {
	switch v := e.Error.(type) {
	case string:
		if v != "" {
			return v
		}
	case nil:
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
	}
	return e.Message
}

// statusError is Bazarr's checked() status mapping, less its inline sleeps:
// a library that sleeps inside Search holds the caller's fetch task hostage
// (captionarr's AckWait is 90 s), so where Bazarr waits out a short delay
// and retries, this returns the error with RetryAfter set to that delay,
// and subtitles.ThrottleFor benches the provider for exactly that long.
//
//	402 -> Config ("Active paid SubDL subscription required"; Bazarr's
//	       ProviderError, which its throttle table holds 1 h for subdl --
//	       the subdl row in subtitles.ThrottleFor).
//	403 -> Auth when the API's own JSON verdict names the key; otherwise an
//	       edge/WAF 403, which is transient: APIThrottled.
//	404 -> ErrRejected (see errNotFoundRoute).
//	429 -> daily_limit / api_download_limit_exceeded: DownloadLimitExceeded
//	       until the next 00:00 UTC plus an hour (the quota resets at
//	       midnight GMT; Bazarr's table: "until 00:00 GMT +1 h");
//	       rate_limit: APIThrottled after Retry-After (15 min without one);
//	       service_busy: ServiceUnavailable after Retry-After (5 s without
//	       one); anything else: APIThrottled.
//	other 4xx -> ErrRejected, about this request only.
//	other     -> ServiceUnavailable.
func (p *Provider) statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	var payload errorPayload
	parsed := json.Unmarshal(body, &payload) == nil
	msg := payload.text()
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	pe := func(kind string, retryAfter time.Duration) *subtitles.ProviderError {
		return &subtitles.ProviderError{
			Provider: p.Name(), Kind: kind, RetryAfter: retryAfter,
			Err: fmt.Errorf("http %d: %s", resp.StatusCode, msg),
		}
	}
	switch code := resp.StatusCode; {
	case code == http.StatusPaymentRequired:
		return pe(subtitles.KindConfig, 0)
	case code == http.StatusForbidden:
		// Only a JSON verdict from the API itself may bench the provider
		// for 12 hours; an edge/WAF 403 says nothing about the key.
		verdict := strings.ToLower(payload.text())
		if parsed && (strings.Contains(verdict, "key") || strings.Contains(verdict, "auth") || strings.Contains(verdict, "forbidden")) {
			return pe(subtitles.KindAuth, 0)
		}
		return pe(subtitles.KindAPIThrottled, 0)
	case code == http.StatusNotFound:
		return errNotFoundRoute
	case code == http.StatusTooManyRequests:
		errCode := ""
		if s, ok := payload.Error.(string); ok {
			errCode = s
		}
		switch errCode {
		case "daily_limit", "api_download_limit_exceeded":
			now := p.now().UTC()
			reset := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
			e := pe(subtitles.KindDownloadLimitExceeded, reset.Add(time.Hour).Sub(now))
			e.ResetAt = reset
			return e
		case "rate_limit":
			return pe(subtitles.KindAPIThrottled, retryAfter(resp, 0))
		case "service_busy":
			return pe(subtitles.KindServiceUnavailable, retryAfter(resp, 5*time.Second))
		}
		return pe(subtitles.KindAPIThrottled, 0)
	case code >= 400 && code < 500:
		return fmt.Errorf("%w (%d): %s", ErrRejected, code, msg)
	default:
		return pe(subtitles.KindServiceUnavailable, 0)
	}
}

// retryAfter reads a delay-seconds Retry-After header, else def. An
// HTTP-date form is ignored (Bazarr's _sleep_for_retry does the same).
func retryAfter(resp *http.Response, def time.Duration) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && s >= 0 {
		return time.Duration(s) * time.Second
	}
	return def
}

// readCapped reads a 200 body through max, one byte past it so an
// over-long body is refused rather than truncated.
func readCapped(body io.Reader, max int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > max {
		return nil, fmt.Errorf("%w: at least %d bytes", ErrResponseTooLarge, len(raw))
	}
	return raw, nil
}
