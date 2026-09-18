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

package importlist

import (
	"context"
	"sync"
	"time"
)

// tokenRefreshMargin is how far ahead of a token's real expiry Expired
// starts reporting true, so a caller proactively refreshes before a request
// hits a 401 rather than reacting to one.
const tokenRefreshMargin = 5 * time.Minute

// Token is an OAuth access/refresh token pair, as issued by a device-code
// flow.
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// Expired reports whether the token should be treated as expired at now:
// true once now is within five minutes of ExpiresAt (including already
// past it), so a caller refreshes proactively rather than waiting for a
// 401.
func (t Token) Expired(now time.Time) bool {
	return !t.ExpiresAt.After(now.Add(tokenRefreshMargin))
}

// TokenStore persists a single Token across reconciles.
type TokenStore interface {
	// Load returns the stored token. ok is false when no token has been
	// stored yet.
	Load(ctx context.Context) (Token, bool, error)

	// Save persists t, replacing whatever was stored before.
	Save(ctx context.Context, t Token) error
}

// MemoryTokenStore is an in-process TokenStore, for tests and the trivial
// case of a provider that does not need its token to survive a restart.
type MemoryTokenStore struct {
	mu    sync.RWMutex
	token Token
	set   bool
}

// NewMemoryTokenStore returns an empty MemoryTokenStore.
func NewMemoryTokenStore() *MemoryTokenStore {
	return &MemoryTokenStore{}
}

// Load implements TokenStore.
func (s *MemoryTokenStore) Load(context.Context) (Token, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token, s.set, nil
}

// Save implements TokenStore.
func (s *MemoryTokenStore) Save(_ context.Context, t Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token, s.set = t, true
	return nil
}
