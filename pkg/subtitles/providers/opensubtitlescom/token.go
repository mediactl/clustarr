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

package opensubtitlescom

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// TokenCache is where a Provider shares its login token, so N worker
// replicas using one OpenSubtitles.com account log in once between them
// rather than once each: the API allows one login per second, and
// OpenSubtitles rate-limits logins far harder than searches (research note
// §4.3). Spec §6.5 puts this token in the provider's shared KV entry;
// captionarr implements TokenCache over app/caption/throttle.Get (the
// State's JWT and TokenExpiresAt) and app/caption/throttle.SetAuth.
//
// The token is a credential. An implementation must keep it out of logs
// and out of any CRD status.
type TokenCache interface {
	// LoadToken returns the shared token, the API host it was issued with
	// (the login response's base_url, "" when none was followed) and its
	// expiry. An empty token with a nil error means there is none yet. The
	// Provider checks freshness itself, so a cache may return an expired
	// token.
	LoadToken(ctx context.Context) (token, server string, expiresAt time.Time, err error)
	// StoreToken records a token this Provider has just obtained, the API
	// host it was issued with, and when it expires: the JWT's own exp claim
	// when it has one, otherwise the login time plus the documented 24-hour
	// life. The host travels with the token as Bazarr caches oscom_server
	// beside oscom_token, so a replica adopting a VIP account's token also
	// sends it to the VIP host.
	StoreToken(ctx context.Context, token, server string, expiresAt time.Time) error
}

const (
	// tokenLifetime is how long an OpenSubtitles.com JWT is valid (research
	// note §4.3: "JWT valid 24 h"), used when the token's own exp claim
	// cannot be read.
	tokenLifetime = 24 * time.Hour
	// tokenRefreshMargin is how much of that life must remain for a token
	// to be reused: Bazarr caches the token for TOKEN_EXPIRATION_TIME = 12 h
	// of its 24 h life (opensubtitlescom.py), so a token is replaced once it
	// is half-way through.
	tokenRefreshMargin = tokenLifetime - 12*time.Hour
)

// tokenFresh reports whether token may still be used at now. A token whose
// JWT claims carry both iat and exp is fresh for the first half of its own
// life (Bazarr's 12 h of 24 h, whatever life the server grants); otherwise
// it is fresh until tokenRefreshMargin before its expiry -- the exp claim
// when there is one, else expiresAt.
func tokenFresh(token string, expiresAt time.Time, now time.Time) bool {
	if iat, exp, ok := jwtTimes(token); ok {
		if !iat.IsZero() && exp.After(iat) {
			return now.Before(iat.Add(exp.Sub(iat) / 2))
		}
		expiresAt = exp // the token's own claim outranks what a cache recorded
	}
	if expiresAt.IsZero() {
		return false
	}
	return now.Before(expiresAt.Add(-tokenRefreshMargin))
}

// tokenExpiry is when token expires: its JWT exp claim when readable,
// otherwise obtainedAt plus tokenLifetime.
func tokenExpiry(token string, obtainedAt time.Time) time.Time {
	if _, exp, ok := jwtTimes(token); ok {
		return exp
	}
	return obtainedAt.Add(tokenLifetime)
}

// jwtTimes reads the iat and exp claims from token's payload segment. It
// does not verify the signature -- the server does that; the client only
// needs to know when to stop using the token. ok is false unless exp is
// present; iat is zero when absent.
func jwtTimes(token string) (iat, exp time.Time, ok bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	var claims struct {
		IssuedAt  int64 `json:"iat"`
		ExpiresAt int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.ExpiresAt <= 0 {
		return time.Time{}, time.Time{}, false
	}
	if claims.IssuedAt > 0 {
		iat = time.Unix(claims.IssuedAt, 0)
	}
	return iat, time.Unix(claims.ExpiresAt, 0), true
}
