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

package statuspatch

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	idxstatus "github.com/mediactl/clustarr/app/indexer/status"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Patch applies a complete status declaration under mgr.
//
// The caller mutates the seeded apply configuration rather than building one,
// which is what makes the complete-declaration rule enforceable in one place.
// mgr must be k8s.ManagerIndex or k8s.ManagerIndexWorker; anything else
// is a programming error and is refused rather than allowed to claim fields
// no part of the split accounts for.
func Patch(
	ctx context.Context,
	c client.Client,
	mgr k8s.FieldManager,
	idx *indexv1alpha1.Indexer,
	mutate func(*indexac.IndexerStatusApplyConfiguration),
) error {
	seed, err := seedFor(mgr)
	if err != nil {
		return err
	}
	ac := seed(idx.Status)
	if mutate != nil {
		mutate(ac)
	}

	obj := indexac.Indexer(idx.Name, idx.Namespace).WithStatus(ac)
	_, err = k8s.PatchStatus(ctx, c, mgr, obj)
	return err
}

// PatchCAS is [Patch] as a compare-and-swap: it reads key fresh through r
// (an uncached reader), seeds mgr's complete set from that read, lets mutate
// change it, and applies with the read's resourceVersion, redoing all of it
// from a new read on a Conflict.
//
// mutate is handed the FRESH object and must derive everything it sets from
// it -- indexedReleases as fresh.Status.IndexedReleases + n, the escalation
// from RecordSuccess/RecordFailure(fresh.Status) -- because a redo is a new
// read. It returns skip to apply nothing. The returned object is the read
// the applied mutate was given: the "before" PublishTransitions measures
// from.
func PatchCAS(
	ctx context.Context,
	r client.Reader,
	c client.Client,
	mgr k8s.FieldManager,
	key client.ObjectKey,
	mutate func(fresh *indexv1alpha1.Indexer, ac *indexac.IndexerStatusApplyConfiguration) (skip bool),
) (*indexv1alpha1.Indexer, bool, error) {
	seed, err := seedFor(mgr)
	if err != nil {
		return nil, false, err
	}
	return k8s.PatchStatusCAS(ctx, r, c, mgr, key,
		func() *indexv1alpha1.Indexer { return &indexv1alpha1.Indexer{} },
		func(fresh *indexv1alpha1.Indexer) (*indexac.IndexerApplyConfiguration, bool, error) {
			ac := seed(fresh.Status)
			if mutate != nil && mutate(fresh, ac) {
				return nil, true, nil
			}
			return indexac.Indexer(fresh.Name, fresh.Namespace).WithStatus(ac), false, nil
		}, casAttempts)
}

// casAttempts bounds PatchCAS. Contention is the Indexer controller's own
// two managers landing on one Indexer while an operator edits it.
const casAttempts = 8

func seedFor(mgr k8s.FieldManager) (func(indexv1alpha1.IndexerStatus) *indexac.IndexerStatusApplyConfiguration, error) {
	switch mgr {
	case k8s.ManagerIndex:
		return idxstatus.ControllerFields, nil
	case k8s.ManagerIndexWorker:
		return idxstatus.WorkerFields, nil
	default:
		return nil, fmt.Errorf("statuspatch: %q owns no part of Indexer.status", mgr)
	}
}
