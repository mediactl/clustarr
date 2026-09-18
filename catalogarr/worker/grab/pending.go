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

package grab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/quality"
)

// casKeepBestAttempts caps the compare-and-swap retry loop. Every iteration
// is a lost race against another worker, and losing this many in a row means
// something is wrong (a hot-looping peer, a broken bucket) rather than merely
// contended -- better to nak and let the consumer's backoff apply than to spin
// inside a handler holding an ack deadline.
const casKeepBestAttempts = 8

// pendingValue is the clustarr-pending bucket's value: the best candidate seen
// so far for one media key, plus the instant the first candidate arrived.
//
// FirstSeen is the whole point of the record. Spec §8.2 anchors the scheduled
// grab at firstSeen+delay and the Msg-Id at <mediaKey>:<firstSeen>, so a
// better release arriving mid-window replaces the candidate without moving the
// deadline -- otherwise a steady trickle of small upgrades would postpone the
// grab forever, and each replacement would mint a new Msg-Id and schedule a
// second delivery.
type pendingValue struct {
	// Target is the catalog item, or pack parent, the grab is for.
	Target commonv1.MediaRef `json:"target"`

	// Keys narrows a pack to specific Episode names.
	Keys []string `json:"keys,omitempty"`

	// Release is the best candidate seen so far.
	Release commonv1.ReleaseInfo `json:"release"`

	// GrabbedBy records which path chose this candidate, so the scheduled
	// grab attributes the Download the way the caller that queued it meant.
	// Without it every delayed grab would report grabbedBy=search, including
	// the RSS matcher's.
	GrabbedBy downloadv1alpha1.GrabSource `json:"grabbedBy,omitempty"`

	// FirstSeen is when the first candidate for this key arrived. It is
	// preserved across every keep-best replacement.
	FirstSeen time.Time `json:"firstSeen"`
}

func (p pendingValue) candidate() quality.Candidate {
	return quality.Candidate{
		Quality:     p.Release.Quality,
		Revision:    p.Release.Revision,
		FormatScore: int(p.Release.FormatScore),
	}
}

// casKeepBest writes candidate into key only if it beats whatever is already
// there, and returns the value that ended up stored.
//
// The comparison is profile.UpgradeDecision, the same ported
// UpgradableSpecification the import and search paths use, so "better" means
// exactly what it means everywhere else in Clustarr rather than a second,
// subtly different ordering.
//
// Both races are handled by looping rather than failing: ErrKeyExists means
// another worker created the key between the Get and the Create, and
// ErrRevisionMismatch means one updated it between the Get and the Update. In
// both cases the next iteration re-reads the winner and re-decides against it,
// which is the correct outcome -- the loser's candidate is still evaluated,
// just against fresher state.
//
// No per-key TTL is set: events.Default()'s BucketPending carries a seven-day
// bucket TTL already, and a per-key TTL would additionally require the bucket
// to declare LimitMarkerTTL.
func casKeepBest(
	ctx context.Context,
	kv events.KV,
	key string,
	profile quality.Profile,
	candidate pendingValue,
	now time.Time,
) (pendingValue, error) {
	for attempt := range casKeepBestAttempts {
		entry, err := kv.Get(ctx, key)
		switch {
		case errors.Is(err, events.ErrKeyNotFound):
			fresh := candidate
			fresh.FirstSeen = now
			data, mErr := json.Marshal(fresh)
			if mErr != nil {
				return pendingValue{}, fmt.Errorf("grab: marshal pending candidate: %w", mErr)
			}
			if _, cErr := kv.Create(ctx, key, data); cErr != nil {
				if errors.Is(cErr, events.ErrKeyExists) {
					continue
				}
				return pendingValue{}, fmt.Errorf("grab: create pending candidate: %w", cErr)
			}
			return fresh, nil

		case err != nil:
			return pendingValue{}, fmt.Errorf("grab: read pending candidate: %w", err)
		}

		var existing pendingValue
		if uErr := json.Unmarshal(entry.Value, &existing); uErr != nil {
			return pendingValue{}, fmt.Errorf("grab: decode pending candidate (attempt %d): %w", attempt+1, uErr)
		}
		if profile.UpgradeDecision(existing.candidate(), candidate.candidate()) != quality.Upgrade {
			return existing, nil
		}

		merged := candidate
		// The anchor is copied from the incumbent, never from the
		// challenger: the delay is measured from when the FIRST candidate
		// for this key appeared, so a replacement must not restart the
		// clock.
		merged.FirstSeen = existing.FirstSeen
		data, mErr := json.Marshal(merged)
		if mErr != nil {
			return pendingValue{}, fmt.Errorf("grab: marshal pending candidate: %w", mErr)
		}
		if _, uErr := kv.Update(ctx, key, data, entry.Revision); uErr != nil {
			if errors.Is(uErr, events.ErrRevisionMismatch) {
				continue
			}
			return pendingValue{}, fmt.Errorf("grab: update pending candidate: %w", uErr)
		}
		return merged, nil
	}
	return pendingValue{}, fmt.Errorf("grab: pending candidate contended for %d attempts on %q", casKeepBestAttempts, key)
}
