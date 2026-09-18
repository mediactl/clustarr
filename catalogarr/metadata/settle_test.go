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
