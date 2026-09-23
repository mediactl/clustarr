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

package metadata

import (
	"errors"
	"fmt"
	"time"
)

// Sentinel errors every provider client maps its own transport-level
// failures onto, so a caller (the Registry, the gateway) can branch on one
// vocabulary regardless of which provider it is talking to.
var (
	// ErrNotFound means the provider does not have a record for the
	// requested id (an HTTP 404 or the provider's own "no such entity").
	ErrNotFound = errors.New("metadata: not found")
	// ErrRateLimited means the provider is throttling this client.
	// RateLimitedError wraps it with a Retry-After hint.
	ErrRateLimited = errors.New("metadata: rate limited")
	// ErrAuth means the provider rejected the client's credentials (an
	// HTTP 401/403, or an authentication flow -- TVDB's login -- failing
	// outright).
	ErrAuth = errors.New("metadata: authentication failed")
	// ErrUnsupported means the provider cannot perform the requested
	// operation for this input at all (for example TMDB's FindMovie given
	// only ids it has no crosswalk for) -- distinct from ErrNotFound, which
	// means the operation ran but found nothing.
	ErrUnsupported = errors.New("metadata: unsupported operation")
	// ErrDecode means the provider's HTTP response could not be parsed: an
	// empty body, truncated JSON, or bytes that are not valid JSON at all,
	// on a response whose HTTP status otherwise looked like success. Every
	// client wraps its decode failures in this -- including a third-party
	// library's own internal decode error (golang-tmdb, musicbrainzws2) --
	// so a caller can tell "the provider sent something this client could
	// not read" apart from a specific mapped status like ErrNotFound.
	ErrDecode = errors.New("metadata: could not decode provider response")
	// ErrResponseTooLarge means the provider's response body exceeded
	// MaxResponseBytes. It is distinct from ErrDecode: the body was never
	// parsed, because reading it whole would have been the failure.
	ErrResponseTooLarge = errors.New("metadata: response body exceeds size limit")
)

// RateLimitedError is returned in place of a bare ErrRateLimited whenever
// the provider told this client how long to wait, so a caller can schedule
// a retry instead of guessing a backoff.
type RateLimitedError struct {
	Provider   string
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("metadata: %s: rate limited, retry after %s", e.Provider, e.RetryAfter)
}

// Unwrap makes errors.Is(err, ErrRateLimited) true for every RateLimitedError.
func (e *RateLimitedError) Unwrap() error {
	return ErrRateLimited
}
