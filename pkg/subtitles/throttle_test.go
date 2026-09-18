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

package subtitles_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/subtitles"
)

func TestThrottleForDefaultDurations(t *testing.T) {
	tests := []struct {
		kind string
		want time.Duration
	}{
		{subtitles.KindTooManyRequests, time.Hour},
		{subtitles.KindDownloadLimitExceeded, 3 * time.Hour},
		{subtitles.KindServiceUnavailable, 20 * time.Minute},
		{subtitles.KindAPIThrottled, 10 * time.Minute},
		{subtitles.KindParse, 6 * time.Hour},
		{subtitles.KindTimeout, time.Hour},
		{subtitles.KindAuth, 12 * time.Hour},
		{subtitles.KindConfig, 12 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			_, d := subtitles.ThrottleFor("some-provider-with-no-override", &subtitles.ProviderError{Kind: tt.kind})
			assert.Equal(t, tt.want, d)
		})
	}
}

func TestThrottleForOpenSubtitlesOverrides(t *testing.T) {
	_, d := subtitles.ThrottleFor("opensubtitlescom", &subtitles.ProviderError{Kind: subtitles.KindTooManyRequests})
	assert.Equal(t, time.Minute, d, "OS.com's own TooManyRequests override is 1 minute, not the 1 hour default")

	_, d = subtitles.ThrottleFor("opensubtitlescom", &subtitles.ProviderError{Kind: subtitles.KindDownloadLimitExceeded})
	assert.Equal(t, 6*time.Hour, d)
}

func TestThrottleForHonoursAnExplicitRetryAfter(t *testing.T) {
	// Gestdown's 423 "refreshing, retry in 30s" (research note §4.2): the
	// provider sets RetryAfter itself, which must win over any table value.
	_, d := subtitles.ThrottleFor("gestdown", &subtitles.ProviderError{Kind: subtitles.KindServiceUnavailable, RetryAfter: 30 * time.Second})
	assert.Equal(t, 30*time.Second, d)
}

func TestIsQuotaExceededAndIsRateLimited(t *testing.T) {
	assert.True(t, subtitles.IsQuotaExceeded(&subtitles.ProviderError{Kind: subtitles.KindDownloadLimitExceeded}))
	assert.False(t, subtitles.IsQuotaExceeded(&subtitles.ProviderError{Kind: subtitles.KindTooManyRequests}))
	assert.True(t, subtitles.IsRateLimited(&subtitles.ProviderError{Kind: subtitles.KindTooManyRequests}))
	assert.False(t, subtitles.IsRateLimited(errors.New("not a ProviderError")))
}

func TestIsNotFound(t *testing.T) {
	assert.True(t, subtitles.IsNotFound(&subtitles.ProviderError{Kind: subtitles.KindNotFound}))
	assert.False(t, subtitles.IsNotFound(&subtitles.ProviderError{Kind: subtitles.KindAuth}))
	assert.False(t, subtitles.IsNotFound(errors.New("not a ProviderError")))
}

func TestThrottleForKindNotFoundHasADefaultDuration(t *testing.T) {
	// KindNotFound is additive to spec §7/research note §10's verbatim
	// 8-kind table — needed for Gestdown's show-lookup 404 (fix round 1,
	// item 6) — so it has no note-mandated duration; a show that doesn't
	// exist on the provider isn't going to appear again soon, so it gets
	// the same long, low-churn backoff as the other persistent-until-
	// reconfigured kinds (Auth, Config).
	_, d := subtitles.ThrottleFor("gestdown", &subtitles.ProviderError{Kind: subtitles.KindNotFound})
	assert.Equal(t, 12*time.Hour, d)
}
