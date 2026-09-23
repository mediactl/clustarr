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

package throttle_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/captionarr/throttle"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// testKV mirrors indexarr/search/limits_test.go's testKV: a bucket bound to
// an in-memory bus, for the tests that exercise this package's logic rather
// than the KV key grammar itself (kvkey_contract_test.go needs a real
// server; the CAS arithmetic and merge semantics below do not).
func testKV(t *testing.T) events.KV {
	t.Helper()
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(t.Context(), events.Default()))
	return bus.KV(events.BucketProviderThrottle)
}

func TestGetReturnsTheZeroStateForAProviderNeverObserved(t *testing.T) {
	kv := testKV(t)
	st, err := throttle.Get(t.Context(), kv, "never-seen")
	require.NoError(t, err)
	assert.Equal(t, throttle.State{}, st)
	assert.False(t, st.Throttled(time.Now()), "a provider with no recorded state is not throttled")
}

func TestRecordErrorSetsThrottledUntilFromThrottleFor(t *testing.T) {
	kv := testKV(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	cause := &subtitles.ProviderError{Provider: "gestdown", Kind: subtitles.KindServiceUnavailable}
	st, err := throttle.RecordError(t.Context(), kv, "gestdown", "uid-1", cause, now)
	require.NoError(t, err)

	wantReason, wantDur := subtitles.ThrottleFor("gestdown", cause)
	require.Equal(t, subtitles.KindServiceUnavailable, wantReason)
	require.Equal(t, 20*time.Minute, wantDur, "premise: ServiceUnavailable's default duration is 20m")

	require.NotNil(t, st.ThrottledUntil)
	assert.Equal(t, now.Add(wantDur), *st.ThrottledUntil)
	assert.Equal(t, wantReason, st.ThrottleReason)
	assert.True(t, st.Throttled(now.Add(10*time.Minute)))
	assert.False(t, st.Throttled(now.Add(21*time.Minute)))

	// Get reflects the same write.
	got, err := throttle.Get(t.Context(), kv, "uid-1")
	require.NoError(t, err)
	assert.Equal(t, st, got)
}

// A worker observing OpenSubtitles' OWN one-minute TooManyRequests override
// must not have that short window overwrite a longer throttle a previous,
// more serious error already set -- the whole point of "never shortening" is
// that a worse condition sticks until it genuinely expires.
func TestRecordErrorNeverShortensAnExistingLongerThrottle(t *testing.T) {
	kv := testKV(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	// DownloadLimitExceeded on opensubtitlescom: 6h override.
	long := &subtitles.ProviderError{Provider: "opensubtitlescom", Kind: subtitles.KindDownloadLimitExceeded}
	st, err := throttle.RecordError(t.Context(), kv, "opensubtitlescom", "uid-2", long, now)
	require.NoError(t, err)
	require.NotNil(t, st.ThrottledUntil)
	longUntil := *st.ThrottledUntil
	assert.Equal(t, now.Add(6*time.Hour), longUntil)

	// TooManyRequests on opensubtitlescom: 1m override, five minutes later.
	short := &subtitles.ProviderError{Provider: "opensubtitlescom", Kind: subtitles.KindTooManyRequests}
	later := now.Add(5 * time.Minute)
	st, err = throttle.RecordError(t.Context(), kv, "opensubtitlescom", "uid-2", short, later)
	require.NoError(t, err)
	require.NotNil(t, st.ThrottledUntil)
	assert.Equal(t, longUntil, *st.ThrottledUntil,
		"a 1m throttle 5 minutes into a 6h throttle must not shorten it to now+1m")
	// ThrottleReason still reflects the still-live longer throttle, not the
	// short one that just arrived and changed nothing.
	assert.Equal(t, subtitles.KindDownloadLimitExceeded, st.ThrottleReason)
}

// A LATER, genuinely longer throttle must still win over an earlier one that
// has not expired yet -- the "never shortens" rule is about the resulting
// UNTIL time, not about refusing every second write.
func TestRecordErrorExtendsWhenTheNewThrottleIsGenuinelyLonger(t *testing.T) {
	kv := testKV(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	short := &subtitles.ProviderError{Provider: "gestdown", Kind: subtitles.KindAPIThrottled} // 10m
	_, err := throttle.RecordError(t.Context(), kv, "gestdown", "uid-3", short, now)
	require.NoError(t, err)

	long := &subtitles.ProviderError{Provider: "gestdown", Kind: subtitles.KindAuth} // 12h
	st, err := throttle.RecordError(t.Context(), kv, "gestdown", "uid-3", long, now.Add(time.Minute))
	require.NoError(t, err)
	require.NotNil(t, st.ThrottledUntil)
	assert.Equal(t, now.Add(time.Minute).Add(12*time.Hour), *st.ThrottledUntil)
	assert.Equal(t, subtitles.KindAuth, st.ThrottleReason)
}

// ErrorStrikeThreshold errors inside ErrorStrikeWindow escalate to at least
// the one-hour floor, even when each individual error's own ThrottleFor
// duration is shorter.
func TestRecordErrorEscalatesAfterFiveStrikesIn120Seconds(t *testing.T) {
	kv := testKV(t)
	base := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	cause := &subtitles.ProviderError{Provider: "opensubtitlescom", Kind: subtitles.KindTooManyRequests} // 1m override

	var st throttle.State
	var err error
	for i := range throttle.ErrorStrikeThreshold {
		at := base.Add(time.Duration(i) * 10 * time.Second) // five strikes across 40s, well inside the 120s window
		st, err = throttle.RecordError(t.Context(), kv, "opensubtitlescom", "uid-4", cause, at)
		require.NoError(t, err)
	}

	require.Equal(t, int32(throttle.ErrorStrikeThreshold), st.ErrorsLast120s)
	require.NotNil(t, st.ThrottledUntil)
	lastAt := base.Add(time.Duration(throttle.ErrorStrikeThreshold-1) * 10 * time.Second)
	assert.True(t, st.ThrottledUntil.After(lastAt.Add(time.Minute)),
		"the strike floor must outlast opensubtitlescom's own 1m TooManyRequests override")
	assert.GreaterOrEqual(t, st.ThrottledUntil.Sub(lastAt), 59*time.Minute,
		"the strike floor is documented as ~1h from the triggering error")
}

// Errors outside the 120s window must not count toward the strike
// threshold -- the window is a sliding one, not a running total (the same
// property indexarr/search's query ring proves for itself).
func TestErrorRingPrunesEntriesOutsideTheWindow(t *testing.T) {
	kv := testKV(t)
	base := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	cause := &subtitles.ProviderError{Provider: "gestdown", Kind: subtitles.KindTimeout}

	for i := range 4 {
		_, err := throttle.RecordError(t.Context(), kv, "gestdown", "uid-5", cause, base.Add(time.Duration(i)*time.Second))
		require.NoError(t, err)
	}
	// Long after the window: the four earlier strikes must have aged out, so
	// this fifth one alone must NOT trip the threshold.
	st, err := throttle.RecordError(t.Context(), kv, "gestdown", "uid-5", cause, base.Add(10*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, int32(1), st.ErrorsLast120s, "the four earlier strikes are 10 minutes stale and must have pruned out")
}

func TestRecordSuccessDoesNotClearAnExistingThrottleOrErrorCount(t *testing.T) {
	kv := testKV(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	cause := &subtitles.ProviderError{Provider: "gestdown", Kind: subtitles.KindTimeout}
	before, err := throttle.RecordError(t.Context(), kv, "gestdown", "uid-6", cause, now)
	require.NoError(t, err)
	require.NotNil(t, before.ThrottledUntil)

	after, err := throttle.RecordSuccess(t.Context(), kv, "uid-6", now.Add(time.Second))
	require.NoError(t, err)
	require.NotNil(t, after.ThrottledUntil)
	assert.Equal(t, *before.ThrottledUntil, *after.ThrottledUntil,
		"a success must not silently clear an in-flight throttle window")
	assert.Equal(t, before.ErrorsLast120s, after.ErrorsLast120s)
	require.NotNil(t, after.LastSuccessAt)
	assert.Equal(t, now.Add(time.Second), *after.LastSuccessAt)
}

// SetQuota and SetAuth are separate mutators against the SAME KV value; each
// must merge rather than clobber the other's fields -- the hazard this
// package's mutateState exists to prevent, the same discipline
// captionarr/status documents for server-side apply's complete-declaration
// rule, applied here to a hand-rolled JSON blob instead of SSA.
func TestSetQuotaAndSetAuthMergeRatherThanClobber(t *testing.T) {
	kv := testKV(t)
	resetAt := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)

	_, err := throttle.SetQuota(t.Context(), kv, "uid-7", 42, resetAt)
	require.NoError(t, err)

	tokenExpiry := time.Date(2026, 9, 23, 18, 0, 0, 0, time.UTC)
	st, err := throttle.SetAuth(t.Context(), kv, "uid-7", "jwt-value", tokenExpiry)
	require.NoError(t, err)

	require.NotNil(t, st.Quota, "SetAuth must not have erased the quota SetQuota wrote")
	assert.Equal(t, int32(42), st.Quota.Remaining)
	require.NotNil(t, st.Quota.ResetAt)
	assert.Equal(t, resetAt, *st.Quota.ResetAt)
	assert.Equal(t, "jwt-value", st.JWT)
	require.NotNil(t, st.TokenExpiresAt)
	assert.Equal(t, tokenExpiry, *st.TokenExpiresAt)
}

// Concurrent RecordError calls against ONE provider from several goroutines
// must not lose updates -- the CAS retry loop, not a mutex, is what makes
// this package usable from N worker replicas that share nothing but the KV
// bucket. This is the in-process analogue of the real-server concurrency
// proof in kvkey_contract_test.go.
func TestConcurrentRecordErrorCallsDoNotLoseUpdates(t *testing.T) {
	kv := testKV(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	cause := &subtitles.ProviderError{Provider: "gestdown", Kind: subtitles.KindTimeout}

	const n = 12
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := throttle.RecordError(context.Background(), kv, "gestdown", "uid-race", cause,
				now.Add(time.Duration(i)*time.Millisecond))
			assert.NoError(t, err)
		}(i)
	}
	wg.Wait()

	st, err := throttle.Get(t.Context(), kv, "uid-race")
	require.NoError(t, err)
	// All n strikes land within the same second, well inside the 120s
	// window and under maxErrorRingEntries, so every one of them must be
	// present -- a lost update would undercount.
	assert.Equal(t, int32(n), st.ErrorsLast120s, "a CAS conflict must retry, not silently drop the write")
}
