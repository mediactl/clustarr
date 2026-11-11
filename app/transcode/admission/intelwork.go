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

package admission

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// The Lease the admission controller publishes its intel work in, for
// cluster-plex's CPU video route: one annotation, one holder (the leader),
// renewed every IntelWorkRenew while the count is above zero and stale to
// its reader after IntelWorkStale (cluster-plex reads it by the rule
// clustarr reads cluster-plex's video-transcode Lease, videolease.go).
const (
	IntelWorkAnnotation = "clustarr.io/intel-work"
	IntelWorkRenew      = 10 * time.Second
	IntelWorkStale      = 30 * time.Second
)

// IntelWorkLease publishes a pass's intel work (PassResult.IntelWork) into
// Key: Set writes when the count moves, Start renews it while it is above
// zero. The Lease is the manager's own, in its own namespace, where the
// leader-election Role grants the writes; a write that fails is logged and
// kept for the next try, never returned to a pass.
type IntelWorkLease struct {
	Client client.Client
	// Reader reads the Lease before each write: the manager's APIReader,
	// so a write never conflicts on its own cache's lag. nil: Client.
	Reader   client.Reader
	Key      types.NamespacedName
	Identity string           // the holder: this pod's name
	Now      func() time.Time // nil: time.Now
	Every    time.Duration    // the renewal period; 0: IntelWorkRenew

	mu    sync.Mutex // the count
	n     int
	known bool
	// publishMu serialises the publishes: a Set's and a renewal's, so a
	// renewal in flight with an older count never lands after the Set that
	// moved it (its conflict retry would resurrect the old count with a
	// fresh renew time, and cluster-plex would read work for 30 s more).
	publishMu sync.Mutex
}

// publishTimeout bounds one publish, so a hung apiserver holds a pass no
// longer than this (cluster-plex's counter bounds its writes the same way).
const publishTimeout = 5 * time.Second

func (l *IntelWorkLease) now() time.Time {
	if l.Now == nil {
		return time.Now()
	}
	return l.Now()
}

// NeedLeaderElection makes the renewer leader-only: the leader's passes
// are the ones that count.
func (l *IntelWorkLease) NeedLeaderElection() bool { return true }

// Set records n and publishes it when it differs from the last count set
// (or on the first call); a failed publish is logged and the value kept.
func (l *IntelWorkLease) Set(ctx context.Context, n int) {
	l.mu.Lock()
	changed := !l.known || n != l.n
	l.n, l.known = n, true
	l.mu.Unlock()
	if !changed {
		return
	}
	if err := l.publish(ctx); err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "transcode admission: cannot publish the intel-work Lease", "lease", l.Key.String(), "count", n, "error", err)
	}
}

// publish is Publish under publishTimeout.
func (l *IntelWorkLease) publish(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	return l.Publish(ctx)
}

// Publish writes the count, the holder and a fresh renew time into the
// Lease, creating it when missing, retrying a conflict from a fresh read;
// one publish at a time, each attempt writing the count as it is then.
func (l *IntelWorkLease) Publish(ctx context.Context) error {
	l.publishMu.Lock()
	defer l.publishMu.Unlock()
	reader := l.Reader
	if reader == nil {
		reader = l.Client
	}
	now := l.now()
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		l.mu.Lock()
		n := l.n
		l.mu.Unlock()
		var lease coordinationv1.Lease
		err := reader.Get(ctx, l.Key, &lease)
		create := apierrors.IsNotFound(err)
		if err != nil && !create {
			return fmt.Errorf("get lease: %w", err)
		}
		if create {
			lease = coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Namespace: l.Key.Namespace, Name: l.Key.Name}}
		}
		if lease.Annotations == nil {
			lease.Annotations = map[string]string{}
		}
		lease.Annotations[IntelWorkAnnotation] = strconv.Itoa(n)
		rt, secs, holder := metav1.NewMicroTime(now), int32(IntelWorkStale/time.Second), l.Identity
		lease.Spec.HolderIdentity, lease.Spec.RenewTime, lease.Spec.LeaseDurationSeconds = &holder, &rt, &secs
		if create {
			if err := l.Client.Create(ctx, &lease); err != nil {
				if apierrors.IsAlreadyExists(err) {
					return apierrors.NewConflict(coordinationv1.Resource("leases"), l.Key.Name, err)
				}
				return fmt.Errorf("create lease: %w", err)
			}
			return nil
		}
		if err := l.Client.Update(ctx, &lease); err != nil {
			if apierrors.IsConflict(err) {
				return err
			}
			return fmt.Errorf("update lease: %w", err)
		}
		return nil
	})
}

// Start renews the Lease every Every while the count is above zero, until
// ctx ends; a renewal that fails is logged and tried again next period.
func (l *IntelWorkLease) Start(ctx context.Context) error {
	every := l.Every
	if every <= 0 {
		every = IntelWorkRenew
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			l.mu.Lock()
			n := l.n
			l.mu.Unlock()
			if n <= 0 {
				continue
			}
			if err := l.publish(ctx); err != nil {
				logging.FromContext(ctx).WarnContext(ctx, "transcode admission: cannot renew the intel-work Lease", "lease", l.Key.String(), "count", n, "error", err)
			}
		}
	}
}

// intelWork is the intel class's work in hand and waiting after a pass:
// its slots in use (running: Queued and Running files; a Swapping file
// holds no slot and no GPU, as clustarr_transcode_jobs_active counts it),
// the candidates assigned to it this pass (queued, admitted or not) and,
// while the intel class is usable (intelUsable: an intel node with a
// healthy pool), the holds of gpu files waiting class-less for a GPU slot
// (WaitingForGPU), which the yield holds beside Plex's streams -- on a
// cluster whose intel class is absent or unhealthy such a file waits for
// nvidia, and Plex's conversions have no reason to leave the GPU. Nothing
// without an intel budget: a cluster that never admits to intel has no
// intel work however many files wait. cluster-plex reads the count from
// the Lease the admission controller publishes (IntelWorkLease) to send
// Plex's conversions to its CPU pool while it is above zero (its
// 2026-11-10 CPU video route design, §1).
func intelWork(queued []candidate, running []Slot, holds []hold, budget map[string]int32, intelUsable bool) int {
	if budget[string(transcodev1alpha1.HardwareIntel)] <= 0 {
		return 0
	}
	n := 0
	for _, s := range running {
		if s.Hardware == string(transcodev1alpha1.HardwareIntel) {
			n++
		}
	}
	for _, c := range queued {
		if c.slot.Hardware == string(transcodev1alpha1.HardwareIntel) {
			n++
		}
	}
	if intelUsable {
		for _, h := range holds {
			if h.msg == WaitingForGPU {
				n++
			}
		}
	}
	return n
}
