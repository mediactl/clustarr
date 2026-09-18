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

package importlist_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/importlist"
)

func TestTokenExpiredWithinFiveMinuteMargin(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	tests := map[string]struct {
		expiresAt time.Time
		want      bool
	}{
		"far future":      {expiresAt: now.Add(time.Hour), want: false},
		"inside margin":   {expiresAt: now.Add(4 * time.Minute), want: true},
		"already expired": {expiresAt: now.Add(-time.Minute), want: true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			tok := importlist.Token{ExpiresAt: tt.expiresAt}
			assert.Equal(t, tt.want, tok.Expired(now))
		})
	}
}

func TestMemoryTokenStoreRoundTrip(t *testing.T) {
	store := importlist.NewMemoryTokenStore()
	_, ok, err := store.Load(context.Background())
	require.NoError(t, err)
	assert.False(t, ok)

	want := importlist.Token{AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Now()}
	require.NoError(t, store.Save(context.Background(), want))

	got, ok, err := store.Load(context.Background())
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, want.AccessToken, got.AccessToken)
}
