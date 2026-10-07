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

package k8s

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CASApplyConfiguration is an [ApplyConfiguration] that can carry a
// resourceVersion precondition. Every top-level configuration generated
// into api/applyconfiguration has the method, so a caller passes its
// concrete pointer type and keeps it:
//
//	*catalogac.MovieApplyConfiguration satisfies CASApplyConfiguration[*catalogac.MovieApplyConfiguration]
type CASApplyConfiguration[A any] interface {
	ApplyConfiguration
	WithResourceVersion(value string) A
}

// PatchStatusCAS is [PatchStatus] as a compare-and-swap: it reads key fresh
// through r, hands the object to render, applies what render returns under
// fm with the read's resourceVersion as a precondition, and on a Conflict
// does it all again from a new read, at most attempts times.
//
// It is for a status a manager writes from more than one place -- several
// replicas of a worker, or a worker and a consumer -- where every apply is
// seeded from a read. The complete-declaration rule (CLAUDE.md) stops an
// apply RELEASING another writer's field, but not ROLLING IT BACK: an apply
// built from a read another writer has since overtaken declares every field,
// just with stale values, and server-side apply takes it. A counter loses an
// increment, a cleared pointer reappears or a set one is cleared. The
// precondition turns that into a Conflict, and the redo recomputes from what
// the other writer left, so render must derive EVERYTHING it declares from
// fresh -- increments included (fresh.N + n, never a value computed before
// the call).
//
// r should be an uncached reader (manager.GetAPIReader()): a read from an
// informer cache that lags the write it raced would conflict again on every
// attempt.
//
// render's skip result applies nothing and returns applied=false, for a
// render that finds on the fresh object there is nothing to do (the item it
// meant to record was withdrawn, the count already says so). Its error is
// returned as is, unretried. The returned object is the fresh read the
// applied (or skipped) render was given -- the "before" a caller measures a
// transition from. A NotFound from the read is returned unwrapped so
// apierrors.IsNotFound and client.IgnoreNotFound work on it; attempts that
// all conflict return the last Conflict, still recognisable by
// apierrors.IsConflict.
//
// Five writers predate this helper and keep hand-rolled loops of the same
// shape -- app/transcode/status (writeStatus/patchCAS), app/import/worker/rescan's
// MediaFile apply, and app/catalog/worker/grab's kindops and nonvideo status
// writes. They are equivalent; a new writer should use this one, as the
// remediation loop's one apply does.
func PatchStatusCAS[O client.Object, A CASApplyConfiguration[A]](
	ctx context.Context,
	r client.Reader,
	c client.Client,
	fm FieldManager,
	key client.ObjectKey,
	newObj func() O,
	render func(fresh O) (ac A, skip bool, err error),
	attempts int,
) (fresh O, applied bool, err error) {
	if r == nil {
		r = c
	}
	if attempts < 1 {
		attempts = 1
	}
	for range attempts {
		fresh = newObj()
		if err = r.Get(ctx, key, fresh); err != nil {
			return fresh, false, err
		}
		ac, skip, rerr := render(fresh)
		if rerr != nil {
			return fresh, false, rerr
		}
		if skip {
			return fresh, false, nil
		}
		ac = ac.WithResourceVersion(fresh.GetResourceVersion())
		if _, err = PatchStatus(ctx, c, fm, ac); err == nil {
			return fresh, true, nil
		}
		if !apierrors.IsConflict(err) {
			return fresh, false, err
		}
	}
	return fresh, false, fmt.Errorf("k8s: %s status contended for %d attempts: %w", key, attempts, err)
}
