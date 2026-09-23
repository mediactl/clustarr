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

package download

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// DefaultEngineTeardownTimeout is how long the removeDataOnDelete finalizer
// waits, from the Download's deletionTimestamp, for an engine that is GONE
// to drop grabarr/engine's finalizer before dropping it on the engine's
// behalf (ruling R-6).
//
// It is a judgement, like the reapers' grace period it matches: long enough
// to ride out an engine pod's restart or a rolling update (the engine drops
// its own finalizer the moment it is back and sees the deletion), short
// enough that deleting a Download whose DownloadClient no longer exists
// does not hang for longer than an operator will wait. A LIVE engine is
// waited for without a bound -- it will act on the deletion -- so this only
// ever applies when [Reconciler.engineGone] says there is nobody to wait
// for.
const DefaultEngineTeardownTimeout = 10 * time.Minute

// ReasonEngineGone is the Warning Event reason when the controller drops the
// engine finalizer on a gone engine's behalf.
const ReasonEngineGone = "EngineGone"

func (r *Reconciler) engineTeardownTimeout() time.Duration {
	if r.EngineTeardownTimeout > 0 {
		return r.EngineTeardownTimeout
	}
	return DefaultEngineTeardownTimeout
}

// engineGone reports whether no engine will ever act on dl's deletion, and
// why: the Download was never pinned to an engine, its DownloadClient no
// longer exists (the engine workload is owned by it and went with it), that
// client's engine is not ready, or dl's engine ordinal is at or past the
// client's current spec.replicas (scaled away; spec §6.3). Anything else is
// a live engine, which the finalizer waits for.
//
// "Not ready" counts as gone only because the caller also waits out
// [DefaultEngineTeardownTimeout] first: a restart is gone for a minute and
// then back, and it drops its own finalizer when it is.
func (r *Reconciler) engineGone(ctx context.Context, dl *downloadv1alpha1.Download) (bool, string, error) {
	if dl.Status.Engine == "" || dl.Spec.ClientRef == "" {
		return true, "the Download was never assigned to an engine", nil
	}
	var dc downloadv1alpha1.DownloadClient
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: dl.Namespace, Name: dl.Spec.ClientRef}, &dc); err != nil {
		if apierrors.IsNotFound(err) {
			return true, fmt.Sprintf("DownloadClient %q no longer exists", dl.Spec.ClientRef), nil
		}
		return false, "", fmt.Errorf("download: get DownloadClient %s: %w", dl.Spec.ClientRef, err)
	}
	if !k8s.IsConditionTrue(dc.Status.Conditions, downloadv1alpha1.DownloadClientConditionEngineReady) {
		return true, fmt.Sprintf("DownloadClient %q engine is not ready", dc.Name), nil
	}
	if ordinal, ok := engineOrdinal(dl.Status.Engine); ok && ordinal >= max(dc.Spec.Replicas, 1) {
		return true, fmt.Sprintf("engine %s is past DownloadClient %q's %d replica(s)", dl.Status.Engine, dc.Name, dc.Spec.Replicas), nil
	}
	return false, "", nil
}

// engineOrdinal reads the ordinal off "<client>-<ordinal>", splitting at the
// last hyphen because a client name may contain its own.
func engineOrdinal(engine string) (int32, bool) {
	i := strings.LastIndex(engine, "-")
	if i < 0 || i == len(engine)-1 {
		return 0, false
	}
	n, err := strconv.ParseInt(engine[i+1:], 10, 32)
	if err != nil || n < 0 {
		return 0, false
	}
	return int32(n), true
}
