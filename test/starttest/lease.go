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

package starttest

import (
	"context"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/k8s"
)

// HoldLease pre-creates a coordination.k8s.io Lease owned by someone else, so
// a manager configured for leader election with that ID can never acquire it.
//
// It is how a start test reproduces the half of a rolling update that
// actually deadlocked: the new pod is up, the old pod still holds the lease,
// and the new pod's readiness has to pass on its own merits.
func HoldLease(t testing.TB, cfg *rest.Config, namespace, id string) {
	t.Helper()
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	renew := metav1.NewMicroTime(time.Now().Add(24 * time.Hour))
	lease := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: namespace},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       new("incumbent-pod"),
			LeaseDurationSeconds: new(int32(86400)),
			AcquireTime:          &renew,
			RenewTime:            &renew,
		},
	}
	if err := c.Create(context.Background(), lease); err != nil {
		t.Fatalf("hold the %s lease: %v", id, err)
	}
}

// ReleaseLease deletes the Lease HoldLease created, so a manager waiting on
// it acquires it on its elector's next retry. A Lease already gone is not
// an error.
func ReleaseLease(t testing.TB, cfg *rest.Config, namespace, id string) {
	t.Helper()
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: namespace}}
	if err := client.IgnoreNotFound(c.Delete(context.Background(), lease)); err != nil {
		t.Fatalf("release the %s lease: %v", id, err)
	}
}
