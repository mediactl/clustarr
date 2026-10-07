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

package remediation

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CreateGraftJob creates a standalone graft Job (loop spec §3.8, §4.12):
// create-only, under the manager's identity. Its name is the dispatch's
// identity, so AlreadyExists is done.
type CreateGraftJob struct{ Job *batchv1.Job }

// DeleteGraftJob deletes an answered or lost graft's Job, Background
// propagation; NotFound is done. (§3.8's list lacks it; §4.12's "record
// answered" row deletes the Job.)
type DeleteGraftJob struct{ Namespace, Name string }

// Kind implements Effect.
func (CreateGraftJob) Kind() EffectKind { return EffectGraftJob }

// Kind implements Effect.
func (DeleteGraftJob) Kind() EffectKind { return EffectGraftJob }

// ExecGraftJobEffect runs e when it is a graft Job effect: ok is false for
// any other effect. The effect executor calls it first. A failure is
// transient: the next pass owes the effect again.
func ExecGraftJobEffect(ctx context.Context, c client.Client, e any) (bool, error) {
	switch e := e.(type) {
	case CreateGraftJob:
		if e.Job == nil {
			return true, nil
		}
		if err := c.Create(ctx, e.Job); err != nil && !apierrors.IsAlreadyExists(err) {
			return true, Transient(fmt.Errorf("create graft Job %s/%s: %w", e.Job.Namespace, e.Job.Name, err))
		}
		return true, nil
	case DeleteGraftJob:
		err := c.Delete(ctx, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: e.Namespace, Name: e.Name}},
			client.PropagationPolicy(metav1.DeletePropagationBackground))
		if client.IgnoreNotFound(err) != nil {
			return true, Transient(fmt.Errorf("delete graft Job %s/%s: %w", e.Namespace, e.Name, err))
		}
		return true, nil
	}
	return false, nil
}
