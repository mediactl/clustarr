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

package throttle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

const (
	// stateCASAttempts bounds the optimistic read-modify-write loop every
	// State mutator runs. Contention here is every fetch worker replica that
	// happens to observe an error or success against the SAME provider at
	// the same moment -- a handful at most, since a SubtitleProvider is
	// typically one upstream account shared by a small worker fleet. Set
	// well above indexarr/search's queryCASAttempts (3): each attempt costs
	// one KV round trip against an infrequently-hit path (only on a worker's
	// error/success observation, never per-request the way [Acquire] is), so
	// a generous ceiling is nearly free and meaningfully de-risks the
	// pathological case where several replicas observe a failure against the
	// same provider in the same instant (state_test.go's own concurrency
	// proof runs a dozen goroutines against one key and must not flake).
	stateCASAttempts = 25

	// ErrorStrikeThreshold and ErrorStrikeWindow are Bazarr's "5-strikes-in-
	// 120s" rule (research note §10, docs/research/subtitles.md:390): an
	// in-memory counter that throttles a provider immediately once this many
	// failures land inside this window, on top of whatever
	// [subtitles.ThrottleFor] already computed for each one individually.
	//
	// In Bazarr this rule matters because its own base ProviderError kind is
	// explicitly "not throttled" (the same research note's table, row
	// "ProviderError"): a single occurrence does nothing on its own, and the
	// strike counter is what stops a fast failure loop. This codebase's
	// [subtitles.ProviderError] has no such unthrottled kind --
	// [subtitles.ThrottleFor] returns a real duration (1h, the "unknown"
	// case) even for an error it cannot classify -- so every recorded error
	// already extends ThrottledUntil on its own. The strike rule is kept as
	// a FLOOR on top of that: reaching the threshold raises ThrottledUntil to
	// at least errorStrikeFloor from now, so a run of individually
	// short-lived throttles (OpenSubtitles' own TooManyRequests override is
	// one minute) cannot let a misbehaving worker re-hit the same provider
	// every sixty seconds forever.
	ErrorStrikeThreshold = 5
	ErrorStrikeWindow    = 120 * time.Second

	// errorStrikeFloor is the minimum throttle applied once
	// ErrorStrikeThreshold is reached within ErrorStrikeWindow. One hour
	// matches the table's own default TooManyRequests/Timeout duration --
	// the same order of magnitude Bazarr already treats as "back off for a
	// while", not a new number invented for this rule.
	errorStrikeFloor = time.Hour

	// maxErrorRingEntries caps the error timestamp ring the same way
	// indexarr/search's maxQueryRingEntries caps the query ring: a provider
	// failing far faster than ErrorStrikeThreshold would ever need cannot
	// turn this KV value into an unbounded blob. Errors prune out of the
	// window on every call, so this is a defensive ceiling, not a value
	// reached in normal operation.
	maxErrorRingEntries = 64
)

// Quota is the download allowance a provider last reported, mirrored from
// api/subtitle/v1alpha1.ProviderQuota so the provider controller's
// projection (F-3) is a direct field copy.
type Quota struct {
	Remaining int32      `json:"remaining"`
	ResetAt   *time.Time `json:"resetAt,omitempty"`
}

// State is one provider's throttle/quota/auth snapshot: the JSON value
// stored under [ProviderKey]. It is both what workers merge into via
// [RecordError], [RecordSuccess], [SetQuota] and [SetAuth], and what the
// SubtitleProvider controller reads via [Get] and projects into
// status.throttledUntil/throttleReason/quota/errorsLast120s (ruling R2).
type State struct {
	// ThrottledUntil is when the provider may be tried again. Nil means the
	// provider is not throttled.
	ThrottledUntil *time.Time `json:"throttledUntil,omitempty"`
	// ThrottleReason is the subtitles.Kind* string (or [ErrorStrikeThreshold]'s
	// own reason) that produced ThrottledUntil.
	ThrottleReason string `json:"throttleReason,omitempty"`
	// Quota is the download allowance the provider last reported, nil until
	// one has been.
	Quota *Quota `json:"quota,omitempty"`

	// JWT is a provider's cached login token (OpenSubtitles.com; spec §6.5's
	// "each provider is a gateway-less in-process client with a shared KV
	// token bucket ... holds JWT + remaining/reset"). It is deliberately
	// NEVER projected into SubtitleProvider.status by the provider
	// controller -- credentials do not belong on a CRD -- and exists here
	// purely so N worker replicas share one login instead of each spending
	// the provider's own login rate limit. TokenExpiresAt is JWT's expiry
	// and IS projected (it is a timestamp, not a secret).
	JWT            string     `json:"jwt,omitempty"`
	TokenExpiresAt *time.Time `json:"tokenExpiresAt,omitempty"`
	// APIServer is the API host JWT was issued with -- OpenSubtitles.com's
	// login base_url, "vip-api.opensubtitles.com" for a VIP account -- so a
	// replica adopting the token sends it where the login said to, as
	// Bazarr caches oscom_server beside oscom_token. Empty means the
	// provider's configured endpoint. Not a secret, and not projected.
	APIServer string `json:"apiServer,omitempty"`

	// LastSuccessAt is when a request to this provider last succeeded.
	LastSuccessAt *time.Time `json:"lastSuccessAt,omitempty"`

	// ErrorsLast120s is len(errorTimestamps) after RecordError's most recent
	// prune -- the projected count, ready for
	// status.errorsLast120s.
	ErrorsLast120s int32 `json:"errorsLast120s,omitempty"`

	// errorTimestamps is [RecordError]'s own rolling window bookkeeping
	// (unix seconds, pruned to the last [ErrorStrikeWindow] on every call),
	// mirroring indexarr/search's query ring. It is exported so it
	// round-trips through JSON, but it is this package's internal state, not
	// part of the projection -- callers read ErrorsLast120s instead.
	ErrorTimestamps []int64 `json:"errorTimestamps,omitempty"`
}

// Throttled reports whether the provider should be skipped at now, per spec
// §6.5's "iterate providers by priority skipping throttled".
func (s State) Throttled(now time.Time) bool {
	return s.ThrottledUntil != nil && now.Before(*s.ThrottledUntil)
}

// Get returns providerUID's current State, or the zero State if none has
// been recorded yet -- a provider that has never errored, succeeded or
// authenticated legitimately has no KV entry, and that is a real, normal
// shape rather than a failure the caller must handle specially.
func Get(ctx context.Context, kv events.KV, providerUID string) (State, error) {
	ent, err := kv.Get(ctx, ProviderKey(providerUID))
	if errors.Is(err, events.ErrKeyNotFound) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(ent.Value, &st); err != nil {
		return State{}, fmt.Errorf("throttle: decode state for %q: %w", providerUID, err)
	}
	return st, nil
}

// mutateState runs a read-modify-write compare-and-swap loop against
// providerUID's State, the same shape indexarr/search/limits.go's countQuery
// uses for its ring: Get (or treat absence as a zero State), let fn mutate
// it, then Create or Update depending on whether a live value existed,
// retrying on ErrRevisionMismatch/ErrKeyExists. A value that fails to decode
// is REPLACED rather than left to fail this call forever -- the same
// precedent countQuery sets for an undecodable ring, and for the same
// reason: failing shut here would wedge this provider's throttle accounting
// for the whole 24h bucket TTL.
func mutateState(ctx context.Context, kv events.KV, providerUID string, fn func(*State)) (State, error) {
	key := ProviderKey(providerUID)
	var lastErr error
	for range stateCASAttempts {
		var (
			st   State
			rev  uint64
			have bool
		)
		ent, err := kv.Get(ctx, key)
		switch {
		case err == nil:
			have, rev = true, ent.Revision
			if uerr := json.Unmarshal(ent.Value, &st); uerr != nil {
				logging.FromContext(ctx).Warn("captionarr/throttle: replacing an undecodable provider state",
					"provider", providerUID, "err", uerr)
				st = State{}
			}
		case errors.Is(err, events.ErrKeyNotFound):
		default:
			return State{}, err
		}

		fn(&st)

		val, err := json.Marshal(st)
		if err != nil {
			return State{}, fmt.Errorf("throttle: encode state for %q: %w", providerUID, err)
		}
		if have {
			_, err = kv.Update(ctx, key, val, rev)
		} else {
			_, err = kv.Create(ctx, key, val)
		}
		switch {
		case err == nil:
			return st, nil
		case errors.Is(err, events.ErrRevisionMismatch), errors.Is(err, events.ErrKeyExists):
			lastErr = err
		default:
			return State{}, err
		}
	}
	return State{}, fmt.Errorf("throttle: state CAS for %q gave up after %d attempts: %w",
		providerUID, stateCASAttempts, lastErr)
}

// RecordError merges one provider failure into providerUID's State:
// [subtitles.ThrottleFor]'s duration (never shortening an existing longer
// throttle) and the five-errors-in-120s escalation described on
// [ErrorStrikeThreshold]. providerType is the SubtitleProviderType string
// (e.g. "opensubtitlescom") ThrottleFor keys its per-provider overrides on;
// providerUID is the object identity the KV key is built from -- they are
// deliberately two different strings, since spec §5's KV table keys state by
// UID while the duration table is keyed by provider TYPE.
func RecordError(ctx context.Context, kv events.KV, providerType, providerUID string, cause error, now time.Time) (State, error) {
	reason, dur := subtitles.ThrottleFor(providerType, cause)
	return mutateState(ctx, kv, providerUID, func(st *State) {
		until := now.Add(dur)
		if st.ThrottledUntil == nil || until.After(*st.ThrottledUntil) {
			st.ThrottledUntil = &until
			st.ThrottleReason = reason
		}

		st.ErrorTimestamps = pruneErrorRing(append(st.ErrorTimestamps, now.Unix()), now)
		st.ErrorsLast120s = int32(len(st.ErrorTimestamps))

		if st.ErrorsLast120s >= ErrorStrikeThreshold {
			floor := now.Add(errorStrikeFloor)
			if st.ThrottledUntil == nil || floor.After(*st.ThrottledUntil) {
				st.ThrottledUntil = &floor
			}
		}
	})
}

// pruneErrorRing drops timestamps older than ErrorStrikeWindow and caps the
// ring at maxErrorRingEntries, keeping the newest. Pure so the window
// arithmetic is testable without a KV at all, mirroring
// indexarr/search.pruneQueryRing.
func pruneErrorRing(ring []int64, now time.Time) []int64 {
	cutoff := now.Add(-ErrorStrikeWindow).Unix()
	out := ring[:0:0]
	for _, at := range ring {
		if at >= cutoff {
			out = append(out, at)
		}
	}
	if len(out) > maxErrorRingEntries {
		out = out[len(out)-maxErrorRingEntries:]
	}
	return out
}

// RecordSuccess stamps providerUID's LastSuccessAt. It deliberately does NOT
// clear ThrottledUntil or reset the error ring: a throttle whose window has
// elapsed is already indistinguishable from "not throttled" to
// [State.Throttled], and the error ring self-decays as entries age out of
// ErrorStrikeWindow on the next RecordError -- there is nothing here that
// needs an explicit clear.
func RecordSuccess(ctx context.Context, kv events.KV, providerUID string, now time.Time) (State, error) {
	return mutateState(ctx, kv, providerUID, func(st *State) {
		st.LastSuccessAt = &now
	})
}

// SetQuota merges a freshly-observed download quota into providerUID's
// State.
func SetQuota(ctx context.Context, kv events.KV, providerUID string, remaining int32, resetAt time.Time) (State, error) {
	return mutateState(ctx, kv, providerUID, func(st *State) {
		q := Quota{Remaining: remaining}
		if !resetAt.IsZero() {
			q.ResetAt = &resetAt
		}
		st.Quota = &q
	})
}

// SetAuth caches a freshly-obtained login token for providerUID, and the API
// host it was issued with, so other worker replicas can reuse both instead
// of logging in themselves.
func SetAuth(ctx context.Context, kv events.KV, providerUID, jwt, apiServer string, expiresAt time.Time) (State, error) {
	return mutateState(ctx, kv, providerUID, func(st *State) {
		st.JWT = jwt
		st.APIServer = apiServer
		if expiresAt.IsZero() {
			st.TokenExpiresAt = nil
		} else {
			st.TokenExpiresAt = &expiresAt
		}
	})
}
