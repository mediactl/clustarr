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

package transcodejob

import (
	"context"
	"sort"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/transcode/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	squasharrstatus "github.com/mediactl/clustarr/app/squash/status"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// writeRetries is how many times writeStatus redoes a write that lost a race
// to the other write path before it gives up and returns the Conflict.
const writeRetries = 3

// patchCAS is the reconciler's and the results consumer's one status write
// (spec §18.2): the dead-letter fold, sorted conditions, and ONE complete
// squasharr/status.ControllerFields declaration applied conditional on the
// resourceVersion tj was read at. A write that raced the other path fails
// with a Conflict rather than rolling it back.
//
// st is the whole status to declare -- tj's own, as read, plus this write's
// changes -- and on return holds exactly what was sent, fold included.
// Conditions are rendered once, here: ControllerFields seeds none, and the
// generated WithConditions appends.
//
// It is also the one place the DLQ projector's annotation is folded into the
// conditions (pkg/k8s.MarkDeadLettered): DeadLettered=True while the object
// carries clustarr.io/dead-lettered, absent once it does not. Every status
// write comes through here, so no write can release the condition another
// one set.
func (r *Reconciler) patchCAS(ctx context.Context, tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) error {
	k8s.MarkDeadLettered(tj, &st.Conditions)
	sortConditions(st.Conditions)
	seed := tj.DeepCopy()
	seed.Status = *st.DeepCopy()
	return squasharrstatus.PatchCAS(ctx, r.Client, seed, func(ac *transcodeac.TranscodeJobStatusApplyConfiguration) {
		if len(seed.Status.Conditions) > 0 {
			ac.WithConditions(k8s.ConditionACs(seed.Status.Conditions)...)
		}
	})
}

// writeStatus reads key fresh through the uncached reader, lets change edit
// a copy of its status, and applies it with the read resourceVersion. A
// Conflict -- the other write path got there first -- is redone from a new
// read, up to writeRetries times, and returned after that.
//
// change returns false for nothing to write; a change that leaves the status
// exactly as read writes nothing either. before is the status as read by the
// attempt that decided (for afterWrite's edges); after is the object as
// written, or nil when nothing was.
func (r *Reconciler) writeStatus(ctx context.Context, key types.NamespacedName,
	change func(tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) bool,
) (before transcodev1alpha1.TranscodeJobStatus, after *transcodev1alpha1.TranscodeJob, err error) {
	for range writeRetries {
		var tj transcodev1alpha1.TranscodeJob
		if err = r.reader().Get(ctx, key, &tj); err != nil {
			return transcodev1alpha1.TranscodeJobStatus{}, nil, err
		}
		before = *tj.Status.DeepCopy()
		st := tj.Status.DeepCopy()
		if !change(&tj, st) || equality.Semantic.DeepEqual(before, *st) {
			return before, nil, nil
		}
		if err = r.patchCAS(ctx, &tj, st); apierrors.IsConflict(err) {
			continue
		}
		if err != nil {
			return before, nil, err
		}
		tj.Status = *st
		return before, &tj, nil
	}
	return transcodev1alpha1.TranscodeJobStatus{}, nil, err
}

// sortConditions orders conditions by type, so a write that changed nothing
// renders them identically and the apiserver sees no change.
func sortConditions(c []metav1.Condition) {
	sort.SliceStable(c, func(i, j int) bool { return c[i].Type < c[j].Type })
}
