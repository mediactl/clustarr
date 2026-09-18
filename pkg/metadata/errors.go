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
	// ErrUnsupported means the provider does not implement the requested
	// operation at all (for example MusicBrainz search, deferred by this
	// client until a later task needs it) -- distinct from ErrNotFound,
	// which means the operation ran but found nothing.
	ErrUnsupported = errors.New("metadata: unsupported operation")
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
