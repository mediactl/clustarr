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
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	coordinationv1client "k8s.io/client-go/kubernetes/typed/coordination/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

	"github.com/mediactl/clustarr/pkg/legacynames"
)

// LegacyLeaseIDs are the per-service leases manager.clustarr.io replaces
// (spec 2026-10-06 §5.8). An alias of legacynames.LeaseIDs until OD6
// deletes the gate (Wave U ruling U-R6).
var LegacyLeaseIDs = legacynames.LeaseIDs

// GatedLeaseLock is the manager's LeaseLock with a gate in front of Create
// and Update: while any legacy per-service lease is held (a non-empty
// holderIdentity whose renewTime + leaseDurationSeconds is still ahead),
// both return an error and leader election retries every RetryPeriod. Once
// every legacy lease is free the gate opens for good, so renewals cost no
// extra GET. Get is never gated. It needs only get on leases, which the
// leader-election Role grants. --legacy-lease-check turns it on; it is
// deleted one release after the cutover (OD6).
type GatedLeaseLock struct {
	resourcelock.Interface
	Leases    coordinationv1client.LeasesGetter
	Namespace string
	Legacy    []string
	Now       func() time.Time
	open      atomic.Bool
}

// NewGatedLeaseLock builds the manager's lease lock for id in namespace,
// with an identity controller-runtime would have given it
// (hostname_uuid), behind the legacy-lease gate.
func NewGatedLeaseLock(cfg *rest.Config, namespace, id string) (resourcelock.Interface, error) {
	if namespace == "" || id == "" {
		return nil, errors.New("k8s: a lease lock needs a namespace and an id")
	}
	coord, err := coordinationv1client.NewForConfig(rest.CopyConfig(cfg))
	if err != nil {
		return nil, fmt.Errorf("k8s: coordination client: %w", err)
	}
	host, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("k8s: lease identity: %w", err)
	}
	inner := &resourcelock.LeaseLock{
		LeaseMeta:  metav1.ObjectMeta{Namespace: namespace, Name: id},
		Client:     coord,
		LockConfig: resourcelock.ResourceLockConfig{Identity: host + "_" + string(uuid.NewUUID())},
	}
	return &GatedLeaseLock{Interface: inner, Leases: coord, Namespace: namespace, Legacy: LegacyLeaseIDs}, nil
}

// Create implements resourcelock.Interface.
func (g *GatedLeaseLock) Create(ctx context.Context, ler resourcelock.LeaderElectionRecord) error {
	if err := g.wait(ctx); err != nil {
		return err
	}
	return g.Interface.Create(ctx, ler)
}

// Update implements resourcelock.Interface.
func (g *GatedLeaseLock) Update(ctx context.Context, ler resourcelock.LeaderElectionRecord) error {
	if err := g.wait(ctx); err != nil {
		return err
	}
	return g.Interface.Update(ctx, ler)
}

func (g *GatedLeaseLock) wait(ctx context.Context) error {
	if g.open.Load() {
		return nil
	}
	now := time.Now()
	if g.Now != nil {
		now = g.Now()
	}
	for _, name := range g.Legacy {
		l, err := g.Leases.Leases(g.Namespace).Get(ctx, name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return fmt.Errorf("k8s: read legacy lease %s/%s: %w", g.Namespace, name, err)
		case held(l, now):
			return fmt.Errorf("k8s: legacy lease %s/%s is held by %q; the manager waits for it to be released or to expire (spec §5.8, --legacy-lease-check)",
				g.Namespace, name, *l.Spec.HolderIdentity)
		}
	}
	g.open.Store(true)
	return nil
}

// held reports whether l has a holder whose lease has not run out at now.
func held(l *coordinationv1.Lease, now time.Time) bool {
	s := l.Spec
	if s.HolderIdentity == nil || *s.HolderIdentity == "" || s.RenewTime == nil || s.LeaseDurationSeconds == nil {
		return false
	}
	return s.RenewTime.Add(time.Duration(*s.LeaseDurationSeconds) * time.Second).After(now)
}
