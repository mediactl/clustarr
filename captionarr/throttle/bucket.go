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
)

// defaultRateMilli is applied when a caller passes a non-positive rate --
// defensive only; api/subtitle/v1alpha1.SubtitleProviderSpec.RequestsPerSecondMilli
// itself defaults (and floors) to 5000 via +kubebuilder:default and
// +kubebuilder:validation:Minimum=1, so a real caller should never reach
// this branch.
const defaultRateMilli = 5000

// minAcquireSleep is the floor on [Acquire]'s local wait between CAS
// attempts once a bucket is empty. Without a floor, a rate at or above 1000
// req/s could compute a near-zero sleep and spin the KV with retries; no
// captionarr provider is configured anywhere near that fast (5 req/s is the
// CRD default and every documented provider limit in research note §10 is
// well under it), so this never binds in practice.
const minAcquireSleep = 5 * time.Millisecond

// bucketState is the per-provider token bucket, stored at [TokenBucketKey].
// TokensMilli is milli-tokens (1000 = one full request's worth) so the
// arithmetic stays integer, the same scaling
// SubtitleProviderSpec.RequestsPerSecondMilli itself uses and for the same
// reason: CRD schemas (and, here, just good hygiene for a value shared
// across processes) reject floating point.
type bucketState struct {
	TokensMilli int64     `json:"tokensMilli"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Acquire blocks until one request's worth of budget is available for
// providerUID at rateMilli (SubtitleProviderSpec.RequestsPerSecondMilli --
// thousandths of a request per second) and consumes it, or returns ctx's
// error if it is cancelled first. Burst capacity equals one second's worth
// of tokens (rateMilli itself, in milli-token units), matching the
// rate.NewLimiter(5, 5) shape pkg/subtitles/providers/opensubtitlescom used
// to default to before ruling R3.
//
// This is spec §6.5's "shared KV token bucket ... so N workers never exceed"
// the configured rate: every fetch worker replica, across every pod, calls
// this with the SAME providerUID before every outbound request to that
// provider, so the bucket -- not any per-process limiter -- is the budget.
//
// # The last-token race
//
// Two workers reading the bucket at the same instant can both compute
// "one token available" from the same starting state. Only one Update can
// land at that revision: JetStream (and membus, identically) reject the
// second writer with ErrRevisionMismatch. The loser does not fail Acquire --
// it re-Gets the NOW-current state (which reflects the winner's
// consumption), recomputes, and either finds another token has refilled in
// the interim or falls into the empty-bucket branch and sleeps. So the
// bucket never overspends: at most one caller ever succeeds per available
// token, by construction of compare-and-swap, not by locking.
//
// # Why Acquire sleeps locally rather than polling the server
//
// Once a caller determines the bucket is empty, the ONLY thing that can make
// a token available before another caller's request completes is time
// passing -- nothing spontaneously refills the KV value between writes: only
// this function's own Get/Create/Update calls touch it (a request completing
// does not "return" a token; capacity is time-based, not
// count-of-in-flight). So Acquire computes exactly how long until its own
// arithmetic says a token exists, sleeps that long (context-aware), and only
// THEN re-reads -- which also picks up whatever every other waiting caller
// did in the meantime, so the recomputation is against fresh state rather
// than a stale local guess.
func Acquire(ctx context.Context, kv events.KV, providerUID string, rateMilli int32) error {
	if rateMilli <= 0 {
		rateMilli = defaultRateMilli
	}
	key := TokenBucketKey(providerUID)
	capacityMilli := int64(rateMilli) // burst = 1s of tokens; see doc comment

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		now := time.Now()
		bs, rev, have, err := getBucket(ctx, kv, key)
		if err != nil {
			return err
		}
		if bs.UpdatedAt.IsZero() {
			bs = bucketState{TokensMilli: capacityMilli, UpdatedAt: now}
		}

		elapsedMilli := now.Sub(bs.UpdatedAt).Milliseconds()
		if elapsedMilli < 0 {
			elapsedMilli = 0 // a clock that moved backward refills nothing rather than draining the bucket
		}
		refillMilli := elapsedMilli * int64(rateMilli) / 1000
		tokens := bs.TokensMilli + refillMilli
		if tokens > capacityMilli {
			tokens = capacityMilli
		}

		if tokens >= 1000 {
			next := bucketState{TokensMilli: tokens - 1000, UpdatedAt: now}
			val, err := json.Marshal(next)
			if err != nil {
				return fmt.Errorf("throttle: encode bucket for %q: %w", providerUID, err)
			}
			if have {
				_, err = kv.Update(ctx, key, val, rev)
			} else {
				_, err = kv.Create(ctx, key, val)
			}
			switch {
			case err == nil:
				return nil
			case errors.Is(err, events.ErrRevisionMismatch), errors.Is(err, events.ErrKeyExists):
				continue // someone else won this token; re-read and recompute
			default:
				return err
			}
		}

		// Empty. Sleep for exactly the time our own arithmetic says is
		// needed for one more token, then loop back to Get fresh state.
		neededMilli := 1000 - tokens
		wait := max(time.Duration(neededMilli)*time.Second/time.Duration(rateMilli), minAcquireSleep)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// getBucket reads providerUID's bucket state, treating a missing key as a
// full bucket (have=false, a zero bucketState the caller fills in) rather
// than an error -- the first Acquire call for a provider has nothing to read
// yet, and that is a normal, expected shape.
func getBucket(ctx context.Context, kv events.KV, key string) (bs bucketState, rev uint64, have bool, err error) {
	ent, err := kv.Get(ctx, key)
	switch {
	case err == nil:
		if uerr := json.Unmarshal(ent.Value, &bs); uerr != nil {
			// An undecodable bucket is replaced with a full one rather than
			// wedging every worker's Acquire for this provider forever,
			// mirroring state.go's mutateState.
			return bucketState{}, ent.Revision, true, nil
		}
		return bs, ent.Revision, true, nil
	case errors.Is(err, events.ErrKeyNotFound):
		return bucketState{}, 0, false, nil
	default:
		return bucketState{}, 0, false, err
	}
}
