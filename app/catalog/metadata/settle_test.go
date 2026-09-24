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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

func TestSettlementMapsProviderErrorsToBusActions(t *testing.T) {
	t.Run("nil is nil", func(t *testing.T) {
		require.NoError(t, settlement(nil))
	})
	t.Run("rate limited with a Retry-After becomes Retry honouring it", func(t *testing.T) {
		err := settlement(&pkgmetadata.RateLimitedError{Provider: "tmdb", RetryAfter: 90 * time.Second})
		var re *events.RetryError
		require.ErrorAs(t, err, &re)
		require.Equal(t, 90*time.Second, re.After)
	})
	t.Run("bare ErrRateLimited becomes Retry with the default delay", func(t *testing.T) {
		err := settlement(pkgmetadata.ErrRateLimited)
		var re *events.RetryError
		require.ErrorAs(t, err, &re)
		require.Equal(t, 30*time.Second, re.After)
	})
	t.Run("not found is terminal", func(t *testing.T) {
		err := settlement(pkgmetadata.ErrNotFound)
		var de *events.DiscardError
		require.ErrorAs(t, err, &de)
	})
	t.Run("auth failure is terminal", func(t *testing.T) {
		err := settlement(pkgmetadata.ErrAuth)
		var de *events.DiscardError
		require.ErrorAs(t, err, &de)
	})
	t.Run("an unmapped error passes through for the subscription backoff", func(t *testing.T) {
		want := errors.New("boom")
		require.Same(t, want, settlement(want))
	})
}

// TestSettlementDecidesPerProvider is the X6b fix: Registry.Lookup joins
// every provider's error, errors.Is matches any one of them, and a joined
// not-found used to Discard a task another provider would have answered
// on a retry.
func TestSettlementDecidesPerProvider(t *testing.T) {
	transient := errors.New("tvdb: unexpected status 502")
	unsupported := fmt.Errorf("%w: %q: %w", errors.New("mangadex: not a manga UUID"), "4050-1", pkgmetadata.ErrUnsupported)
	tests := []struct {
		name    string
		err     error
		discard bool
	}{
		{"not found beside a transient failure stays retryable", errors.Join(pkgmetadata.ErrNotFound, transient), false},
		{"credentials rejected beside a transient failure stays retryable", errors.Join(transient, pkgmetadata.ErrAuth), false},
		{"wrapped, the join is still split", fmt.Errorf("lookup: %w", errors.Join(pkgmetadata.ErrNotFound, transient)), false},
		{"every provider saying not found is final", errors.Join(pkgmetadata.ErrNotFound, fmt.Errorf("tmdb: %w", pkgmetadata.ErrNotFound)), true},
		{"not found beside a provider that cannot use the ids is final", errors.Join(pkgmetadata.ErrNotFound, unsupported), true},
		{"one provider's error wrapping two sentinels is one answer", errors.Join(unsupported), false},
		{"a single provider's not found is final", errors.Join(pkgmetadata.ErrNotFound), true},
		{"a not found that wraps its detail with a second %w is one final answer", fmt.Errorf("%w: %w", pkgmetadata.ErrNotFound, errors.New("tmdb: 404 for /movie/1")), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := settlement(tt.err)
			var de *events.DiscardError
			require.Equal(t, tt.discard, errors.As(got, &de), "settlement(%v) = %v", tt.err, got)
			if !tt.discard {
				require.Same(t, tt.err, got, "a retryable error passes through to the subscription backoff")
			}
		})
	}
}
