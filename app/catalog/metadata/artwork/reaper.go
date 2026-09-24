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

package artwork

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jonboulle/clockwork"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

const (
	// DefaultReapInterval is how often the reaper sweeps (spec §B.5). It
	// is level-driven, so the interval decides only how promptly an orphan
	// goes, never whether it does.
	DefaultReapInterval = 6 * time.Hour

	// DefaultReapGrace is how old, by its ModTime, an object must be before
	// the reaper judges it (spec §B.5): the gateway reads an item before
	// fetching its artwork, so a fresh object's item existed moments ago,
	// and the grace covers any read the reaper's list could have raced.
	DefaultReapGrace = 30 * time.Minute

	// reapPageSize bounds one metadata-only List page.
	reapPageSize = 500
)

// Reaper deletes the artwork of items that no longer exist (spec §B.5):
// every object in events.BucketArtwork whose "<kind>/<uid>" prefix names no
// live item of that kind, once the object is older than Grace. Both
// variants go -- it is the only code that deletes an overlay as well as an
// original -- following grabarr's reapers (app/grab/engine/torrent.Reaper):
// level-driven, list against list, so it recovers from a deletion nobody
// observed.
//
// It is conservative on every axis where it cannot be sure. A kind whose
// List fails is skipped whole for the sweep -- an empty answer from a failed
// list is not "no live items". A name that is not <kind>/<uid>/..., or a
// kind with no artwork, is left alone. And UIDs, not names, decide: a
// deleted and re-created item has a new UID, so its predecessor's objects
// are orphans even though the name lives on.
type Reaper struct {
	// Store is Bus.ObjectStore(events.BucketArtwork).
	Store events.ObjectStore

	// Client lists the items. Production passes mgr.GetAPIReader(): an
	// uncached read of metadata only, once per kind per sweep, so a cache
	// that has not synced cannot make every live item look deleted.
	Client client.Reader

	// Interval defaults to DefaultReapInterval, Grace to DefaultReapGrace.
	Interval, Grace time.Duration

	// Clock is the sweep's "now". Nil is the real clock.
	Clock clockwork.Clock
}

// NeedLeaderElection makes the reaper a cluster singleton (spec §B.5): the
// bucket is shared, and two sweepers would only race each other's deletes.
func (r *Reaper) NeedLeaderElection() bool { return true }

func (r *Reaper) clock() clockwork.Clock {
	if r.Clock != nil {
		return r.Clock
	}
	return clockwork.NewRealClock()
}

// Start sweeps at once and then every Interval until ctx is done. A failed
// sweep is logged and retried on the next tick; it never stops the loop.
func (r *Reaper) Start(ctx context.Context) error {
	interval := r.Interval
	if interval <= 0 {
		interval = DefaultReapInterval
	}
	ticker := r.clock().NewTicker(interval)
	defer ticker.Stop()
	for {
		if n, err := r.Sweep(ctx); err != nil {
			logging.FromContext(ctx).Warn("artwork: reap sweep incomplete", "deleted", n, "err", err)
		} else if n > 0 {
			logging.FromContext(ctx).Info("artwork: reaped orphaned objects", "deleted", n)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.Chan():
		}
	}
}

// Sweep runs one pass and returns how many objects it deleted. Its error
// joins every List and Delete that failed; the pass carries on past each.
func (r *Reaper) Sweep(ctx context.Context) (deleted int, err error) {
	ctx, span := tracing.Start(ctx, "artwork.Reaper.Sweep")
	defer span.End()

	grace := r.Grace
	if grace <= 0 {
		grace = DefaultReapGrace
	}
	objects, err := r.Store.List(ctx, "")
	if err != nil {
		tracing.RecordError(span, err)
		return 0, fmt.Errorf("artwork: list the bucket: %w", err)
	}
	now := r.clock().Now()

	var errs []error
	live := map[commonv1.MediaKind]sets.Set[types.UID]{}
	failed := map[commonv1.MediaKind]bool{}
	for _, o := range objects {
		kind, uid, ok := parseKey(o.Name)
		if !ok {
			continue
		}
		if now.Sub(o.ModTime) < grace || failed[kind] {
			continue
		}
		uids, listed := live[kind]
		if !listed {
			uids, err = r.liveUIDs(ctx, kind)
			if err != nil {
				failed[kind] = true
				errs = append(errs, err)
				continue
			}
			live[kind] = uids
		}
		if uids.Has(uid) {
			continue
		}
		if err := r.Store.Delete(ctx, o.Name); err != nil && !errors.Is(err, events.ErrObjectNotFound) {
			errs = append(errs, fmt.Errorf("artwork: delete %s: %w", o.Name, err))
			continue
		}
		deleted++
	}
	err = errors.Join(errs...)
	if err != nil {
		tracing.RecordError(span, err)
	}
	return deleted, err
}

// parseKey reads "<kind>/<uid>/..." back out of an ArtworkKey, reporting
// false for a name of any other shape or a kind with no artwork.
func parseKey(name string) (commonv1.MediaKind, types.UID, bool) {
	parts := strings.SplitN(name, "/", 3)
	if len(parts) < 3 || parts[1] == "" {
		return "", "", false
	}
	kind := commonv1.MediaKind(parts[0])
	if _, ok := gvkFor(kind); !ok {
		return "", "", false
	}
	return kind, types.UID(parts[1]), true
}

// liveUIDs lists every item of kind in every namespace, metadata only.
func (r *Reaper) liveUIDs(ctx context.Context, kind commonv1.MediaKind) (sets.Set[types.UID], error) {
	gvk, _ := gvkFor(kind)
	uids := sets.New[types.UID]()
	cont := ""
	for {
		list := &metav1.PartialObjectMetadataList{}
		list.SetGroupVersionKind(gvk)
		opts := []client.ListOption{client.Limit(reapPageSize)}
		if cont != "" {
			opts = append(opts, client.Continue(cont))
		}
		if err := r.Client.List(ctx, list, opts...); err != nil {
			return nil, fmt.Errorf("artwork: list %s: %w", kind, err)
		}
		for i := range list.Items {
			uids.Insert(list.Items[i].UID)
		}
		cont = list.Continue
		if cont == "" {
			return uids, nil
		}
	}
}
