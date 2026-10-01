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

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/indexer/limits"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// ErrGrabLimitReached is the sentinel a [*GrabLimitError] unwraps to.
var ErrGrabLimitReached = errors.New("grab: the indexer's grab limit is reached")

// GrabLimitError is a grab refused because the release's Indexer already
// holds spec.limits.grabLimit grabs in its window. Nothing is wrong with the
// release: the Sink tries the next-ranked release on another indexer, and
// when none remains -- or on the scheduled path, which holds one candidate --
// the grab is held until RetryAt, when the window next has room.
type GrabLimitError struct {
	// Indexer is the refusing Indexer's name, in the grab's namespace.
	Indexer string

	// RetryAt is when its grab window next has room.
	RetryAt time.Time
}

func (e *GrabLimitError) Error() string {
	return fmt.Sprintf("grab: indexer %q is at its grab limit until %s", e.Indexer, e.RetryAt.UTC().Format(time.RFC3339))
}

func (e *GrabLimitError) Unwrap() error { return ErrGrabLimitReached }

// reserveGrab reserves release's grab on its Indexer's grab ring in
// clustarr-indexer-limits (app/indexer/limits), and refuses it with a
// *GrabLimitError when the window is at spec.limits.grabLimit.
//
// This is where a grab limit has to hold: the Download this grab creates is
// the grab. A direct-source Download never reaches indexarr's download verb,
// and one that does would be refused there only after the Download existed,
// where the refusal reads as the release failing. The ring is keyed by GUID,
// so the verb's own reservation of the same release later, and the
// direct-grab counter's, count nothing twice -- and a redelivered grab of a
// release already reserved passes.
//
// A release that names no Indexer, or one that is gone, has no limit. An
// accounting outage fails open, logged: it must not stop every grab.
func reserveGrab(ctx context.Context, d Deps, ns string, release commonv1.ReleaseInfo) error {
	if d.Bus == nil || release.IndexerRef == "" || release.GUID == "" {
		return nil
	}
	idx, err := getIndexer(ctx, d.Client, ns, release.IndexerRef)
	switch {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return fmt.Errorf("grab: get indexer %q: %w", release.IndexerRef, err)
	}
	now := d.now()
	r, err := limits.ReserveGrab(ctx, d.Bus.KV(events.BucketIndexerLimits), idx, release.GUID, now)
	if err != nil {
		logging.FromContext(ctx).Warn("grab: grab accounting failed; grabbing anyway",
			"indexer", release.IndexerRef, "error", err)
		return nil
	}
	if r.Allowed {
		return nil
	}
	retryAt := r.RetryAt
	if !retryAt.After(now) {
		retryAt = now.Add(grabRetry) // defensive: a refusal always names a future instant
	}
	return &GrabLimitError{Indexer: release.IndexerRef, RetryAt: retryAt}
}
