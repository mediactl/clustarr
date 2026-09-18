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

package subtitles

import (
	"errors"
	"fmt"
	"time"
)

// ProviderError is spec §7's Kind/RetryAfter plus additive quota/identity
// fields the OpenSubtitles /download response and ThrottleFor both need.
type ProviderError struct {
	Provider   string
	Kind       string // one of the Kind* constants below — spec §7's exact 8-name comment, reconciled to Bazarr's real exception names (research note §10)
	RetryAfter time.Duration
	ResetAt    time.Time
	Remaining  int
	Err        error
}

func (e *ProviderError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("subtitles: %s: %s: %v", e.Provider, e.Kind, e.Err)
	}
	return fmt.Sprintf("subtitles: %s: %s", e.Provider, e.Kind)
}
func (e *ProviderError) Unwrap() error { return e.Err }

const (
	KindTooManyRequests       = "TooManyRequests"
	KindDownloadLimitExceeded = "DownloadLimitExceeded"
	KindServiceUnavailable    = "ServiceUnavailable"
	KindAPIThrottled          = "APIThrottled"
	KindParse                 = "Parse"
	KindTimeout               = "Timeout"
	KindAuth                  = "Auth"
	KindConfig                = "Config"
)

func kindOf(err error) (string, bool) {
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe.Kind, true
	}
	return "", false
}

func IsQuotaExceeded(err error) bool {
	k, ok := kindOf(err)
	return ok && k == KindDownloadLimitExceeded
}
func IsRateLimited(err error) bool { k, ok := kindOf(err); return ok && k == KindTooManyRequests }

// defaultDurations is spec §6.5 / research note §10's verbatim table.
var defaultDurations = map[string]time.Duration{
	KindTooManyRequests: time.Hour, KindDownloadLimitExceeded: 3 * time.Hour,
	KindServiceUnavailable: 20 * time.Minute, KindAPIThrottled: 10 * time.Minute,
	KindParse: 6 * time.Hour, KindTimeout: time.Hour, KindAuth: 12 * time.Hour, KindConfig: 12 * time.Hour,
}

// providerOverrides is research note §10's per-provider column.
var providerOverrides = map[string]map[string]time.Duration{
	"opensubtitlescom": {KindTooManyRequests: time.Minute, KindDownloadLimitExceeded: 6 * time.Hour},
}

// ThrottleFor is spec §7's exact signature: the verbatim duration table
// (spec §6.5, research note §10), with a provider-supplied RetryAfter
// (Retry-After header, OpenSubtitles reset_time, Gestdown's 423 30 s hint)
// taking precedence over the static table when present.
func ThrottleFor(provider string, err error) (reason string, d time.Duration) {
	var pe *ProviderError
	if !errors.As(err, &pe) {
		return "unknown", time.Hour
	}
	if pe.RetryAfter > 0 {
		return pe.Kind, pe.RetryAfter
	}
	if byProvider, ok := providerOverrides[provider]; ok {
		if d, ok := byProvider[pe.Kind]; ok {
			return pe.Kind, d
		}
	}
	return pe.Kind, defaultDurations[pe.Kind]
}
