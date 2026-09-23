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
	"errors"
	"fmt"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// ErrDuplicateGrab reports that some other grab already owns this media key.
// Spec §8.2's rule for it is "on exists -> ack and stop": it is a normal
// outcome of at-least-once delivery and of two workers racing, not a failure,
// so a work handler must acknowledge it rather than nak.
var ErrDuplicateGrab = errors.New("grab: lease already held")

// leaseOrphanGrace is how old a lease whose Download does not exist must be
// before a grab may take it over. It is spec §5's clustarr-leases sweeper
// period: a key "whose Download no longer exists" is reclaimed after ten
// minutes. The grace is what keeps an in-flight grab safe -- performGrab takes
// its leases before it creates the Download, so for that window a lease with
// no Download is live, not orphaned.
const leaseOrphanGrace = 10 * time.Minute

// leaseReclaimAttempts bounds the Create/Get/Update loop for one key. Every
// iteration is a lost race over that key; losing this many means another grab
// is actively taking it, which is exactly a duplicate.
const leaseReclaimAttempts = 3

// holderState is what a lease's holder -- the Download named by its value --
// says about the lease.
type holderState int

const (
	// holderActive: the holder Download still occupies the item, or may be
	// about to (it does not exist yet, inside leaseOrphanGrace). The lease
	// is held.
	holderActive holderState = iota
	// holderStale: the holder Download is terminal, being deleted, or has
	// been missing for longer than leaseOrphanGrace. The lease guards
	// nothing and may be taken over.
	holderStale
)

// holderFunc reports the state of the Download a lease names. entry is the
// lease itself, so the caller can age it.
type holderFunc func(ctx context.Context, entry events.Entry) (holderState, error)

// acquireLeases takes the clustarr-leases key for every status target of one
// grab, all or nothing, and returns the keys this call changed -- the ones a
// rollback may delete.
//
// All-or-nothing is what makes a season pack safe. A pack covering episodes
// 1-10 where episode 4 is already downloading must not half-grab: spec §8.2
// says "for every key (episodes of a pack; all-or-nothing, release taken ones
// on failure)". Partial success would leave a Download that overlaps another
// one, and leases the loser would never clean up.
//
// The primitive is Create-fails-if-exists, which is atomic at the broker, so
// two workers racing over the same key cannot both win. The value is the
// Download name, so an operator reading the bucket can see which grab holds
// what, and so the next grab can ask that Download whether the lease still
// means anything.
//
// A key that already exists is not automatically a duplicate:
//
//   - Held by downloadName itself, it is this grab's own lease from an earlier
//     delivery -- one that created the Download and then failed to clear
//     pendingGrab or to publish release.grabbed. The key is re-entered, not
//     refused, which is what makes the redelivery the idempotent retry the
//     handler relies on; refusing it acknowledged the task as a duplicate and
//     lost the release.grabbed event (and with it indexarr's grab count).
//   - Held by a Download that is terminal, being deleted, or missing past
//     leaseOrphanGrace, it guards nothing and is taken over with a
//     revision-checked Update, so two grabs reclaiming it at once cannot both
//     win. Spec §5 has the Download controller delete the key on a terminal
//     phase and a sweeper delete keys whose Download is gone; neither
//     exists, so without this every item kept its first grab's lease forever
//     and could never be grabbed again -- not after a failed download, not
//     for an upgrade. Reclaiming at the point of contention needs no sweeper
//     and cannot race one.
//   - Held by an active Download, it is ErrDuplicateGrab.
//
// Leases taken by a winning grab are deliberately NOT released here on
// success. They outlive this function until the next grab of the item finds
// its holder stale.
func acquireLeases(ctx context.Context, kv events.KV, keys []string, downloadName string, holder holderFunc) ([]string, error) {
	taken := make([]string, 0, len(keys))
	for _, key := range keys {
		changed, err := acquireLease(ctx, kv, key, downloadName, holder)
		if err != nil {
			releaseLeases(ctx, kv, taken)
			return nil, err
		}
		if changed {
			taken = append(taken, key)
		}
	}
	return taken, nil
}

// acquireLease takes one key. changed is false when the key was already held
// by downloadName: a rollback must not delete a lease this call did not take.
func acquireLease(ctx context.Context, kv events.KV, key, downloadName string, holder holderFunc) (changed bool, err error) {
	for range leaseReclaimAttempts {
		_, err := kv.Create(ctx, key, []byte(downloadName))
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, events.ErrKeyExists) {
			return false, fmt.Errorf("grab: take lease %q: %w", key, err)
		}

		entry, err := kv.Get(ctx, key)
		switch {
		case errors.Is(err, events.ErrKeyNotFound):
			// Deleted between the Create and the Get: try again.
			continue
		case err != nil:
			return false, fmt.Errorf("grab: read lease %q: %w", key, err)
		}
		if string(entry.Value) == downloadName {
			return false, nil
		}

		state, err := holder(ctx, entry)
		if err != nil {
			return false, fmt.Errorf("grab: check holder %q of lease %q: %w", entry.Value, key, err)
		}
		if state != holderStale {
			return false, fmt.Errorf("%w: %s held by %s", ErrDuplicateGrab, key, entry.Value)
		}
		if _, err := kv.Update(ctx, key, []byte(downloadName), entry.Revision); err != nil {
			if errors.Is(err, events.ErrRevisionMismatch) || errors.Is(err, events.ErrKeyNotFound) {
				continue
			}
			return false, fmt.Errorf("grab: reclaim lease %q from %s: %w", key, entry.Value, err)
		}
		logging.FromContext(ctx).Info("grab: reclaimed a stale lease",
			"key", key, "from", string(entry.Value), "to", downloadName)
		return true, nil
	}
	return false, fmt.Errorf("%w: %s contended for %d attempts", ErrDuplicateGrab, key, leaseReclaimAttempts)
}

// FreeLeases deletes the clustarr-leases keys downloadName holds on target's
// status targets -- spec §8.3's "deletes the lease" for a Download that
// failed -- and returns the keys it deleted. target is the Download's
// spec.target, Keys included, so a season pack frees every episode's lease.
//
// Only a key whose value is downloadName is deleted. A key held under any
// other name belongs to a later grab (one that already reclaimed the lease
// from this terminal holder) and is left alone; a missing key was never taken
// or is already gone. Both make FreeLeases idempotent, so a redelivered
// failure frees nothing twice.
//
// The value check is not a compare-and-swap: events.KV has no
// revision-checked Delete, so a grab that reclaims the key between this Get
// and this Delete loses its lease. It keeps its Download, and the double-grab
// guard's live Download list (guardExistingDownloads) still refuses a third
// grab of the item once that Download exists, so what is exposed is the
// sub-second gap between that grab's reclaim and its create.
//
// Without FreeLeases nothing is stranded -- acquireLease reclaims a lease
// whose holder is terminal -- but the key keeps naming a dead Download
// until the next grab of the item finds it, which is what an operator
// reading the bucket would see.
func FreeLeases(ctx context.Context, kv events.KV, ns string, target commonv1.MediaRef, downloadName string) ([]string, error) {
	targets, err := StatusTargets(target, target.Keys)
	if err != nil {
		return nil, err
	}
	var freed []string
	for _, key := range leaseKeys(ns, targets) {
		entry, err := kv.Get(ctx, key)
		switch {
		case errors.Is(err, events.ErrKeyNotFound):
			continue
		case err != nil:
			return freed, fmt.Errorf("grab: read lease %q: %w", key, err)
		}
		if string(entry.Value) != downloadName {
			continue
		}
		if err := kv.Delete(ctx, key); err != nil {
			return freed, fmt.Errorf("grab: free lease %q held by %s: %w", key, downloadName, err)
		}
		freed = append(freed, key)
	}
	return freed, nil
}

// releaseLeases deletes every key in acquired, best effort. A failed delete is
// logged and skipped rather than returned: this runs on the rollback path of a
// grab that is already failing, and turning a cleanup error into the caller's
// error would mask the real cause. A lease left behind by a failed delete is
// not permanent either: it names a Download that was never created, so the
// next grab of the item takes it over once leaseOrphanGrace has passed.
func releaseLeases(ctx context.Context, kv events.KV, acquired []string) {
	for _, key := range acquired {
		if err := kv.Delete(ctx, key); err != nil {
			logging.FromContext(ctx).Warn("grab: releasing lease failed; the next grab reclaims it after the orphan grace",
				"key", key, "error", err)
		}
	}
}
