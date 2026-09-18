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
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// defaultRetryAfter is used when a provider's rate-limit error carries no
// Retry-After hint (ErrRateLimited bare, not wrapped in RateLimitedError).
const defaultRetryAfter = 30 * time.Second

// settlement maps a pkg/metadata provider error onto the events.Handler
// vocabulary §8.8 defines: Retry for a transient, provider-told-us-when
// condition; Discard for one no amount of retrying fixes; anything else
// passes through so the subscription's own backoff schedule
// (catalogarr-metadata: 30s,2m,10m,1h,6h) applies.
func settlement(err error) error {
	if err == nil {
		return nil
	}
	var rl *pkgmetadata.RateLimitedError
	if errors.As(err, &rl) {
		after := rl.RetryAfter
		if after <= 0 {
			after = defaultRetryAfter
		}
		return events.Retry(after, err)
	}
	if errors.Is(err, pkgmetadata.ErrRateLimited) {
		return events.Retry(defaultRetryAfter, err)
	}
	if errors.Is(err, pkgmetadata.ErrNotFound) {
		return events.Discard("metadata: no such record at the provider", err)
	}
	if errors.Is(err, pkgmetadata.ErrAuth) {
		return events.Discard("metadata: provider rejected credentials", err)
	}
	return err
}
