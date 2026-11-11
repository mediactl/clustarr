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
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	coordinationv1 "k8s.io/api/coordination/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// The intel class's work in hand and waiting (cluster-plex's 2026-11-10
// CPU video route design, §1): running, queued and Planned intel files and
// the yield's held gpu files; never grafts, cpu or nvidia, and nothing at
// all without an intel budget.
func TestIntelWorkCountsTheIntelClassInHandAndWaiting(t *testing.T) {
	intelSlot := func(k string) Slot { return Slot{Key: k, Hardware: string(intel)} }
	running := []Slot{intelSlot("a"), intelSlot("b"), {Key: "c", Hardware: "cpu"}, {Key: "d", Hardware: "nvidia"}}
	queued := []candidate{{slot: intelSlot("e")}, {slot: Slot{Key: "f", Hardware: "cpu"}}}
	holds := []hold{{msg: WaitingForGPU}, {msg: "waiting: the pool is unhealthy"}}
	budget := map[string]int32{"intel": 2, "cpu": 2}
	assert.Equal(t, 4, intelWork(queued, running, holds, budget), "two running, one queued, one held gpu file")
	assert.Equal(t, 0, intelWork(queued, running, holds, map[string]int32{"cpu": 2}), "no intel budget is no intel work")
	assert.Equal(t, 0, intelWork(queued, running, holds, map[string]int32{"intel": 0}))
}

func TestNoIntelBudgetIsNoIntelWork(t *testing.T) {
	l, _ := newTestLedger(t, map[string]int32{"cpu": 2}, 0)
	gpu := fv("g", 1, planned)
	gpu.Hardware = transcodev1alpha1.HardwareGPU
	l.Observe(gpu)
	res := l.Pass(passIn())
	assert.Zero(t, res.IntelWork)
}

func TestAPassReportsItsIntelWork(t *testing.T) {
	l, _ := newTestLedger(t, map[string]int32{"cpu": 0, "intel": 2}, 0)
	r := fv("r", 1, running)
	r.Class = intel
	l.Observe(r)
	l.Observe(fv("a", 2, planned)) // auto: competes for intel on an Intel node
	j := fv("j", 3, planned)
	j.Hardware = intel // pinned intel, beyond the budget this pass
	l.Observe(j)
	res := l.Pass(intelIn(0))
	assert.Equal(t, 1, l.dispatchGrants(intel), "one slot is left beside the running encode")
	assert.Equal(t, 3, res.IntelWork, "the running encode, the admitted file and the one still waiting")

	held, _ := newTestLedger(t, map[string]int32{"cpu": 0, "intel": 2}, 0)
	g := fv("g", 1, planned)
	g.Hardware = transcodev1alpha1.HardwareGPU
	held.Observe(g)
	res = held.Pass(intelIn(1))
	assert.Equal(t, YieldingToPlex, held.Snapshot("g").WaitReason)
	assert.Equal(t, 1, res.IntelWork, "a gpu file the yield holds is intel work waiting")
}

// The Lease that publishes the count: written when it moves, renewed
// every Every while above zero, one holder, a failure logged not returned.

func intelWorkLease(t *testing.T, c client.Client, identity string, now *time.Time) *IntelWorkLease {
	t.Helper()
	return &IntelWorkLease{
		Client: c, Key: types.NamespacedName{Namespace: "clustarr-system", Name: "clustarr-intel-work"},
		Identity: identity, Now: func() time.Time { return *now },
	}
}

func readLease(t *testing.T, c client.Client, key types.NamespacedName) *coordinationv1.Lease {
	t.Helper()
	var l coordinationv1.Lease
	require.NoError(t, c.Get(t.Context(), key, &l))
	return &l
}

func TestTheIntelWorkLeaseIsWrittenWhenTheCountMoves(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	now := time.Date(2026, 11, 10, 23, 0, 0, 0, time.UTC)
	l := intelWorkLease(t, c, "clustarr-manager-1", &now)
	l.Set(t.Context(), 3)
	got := readLease(t, c, l.Key)
	assert.Equal(t, "3", got.Annotations[IntelWorkAnnotation])
	assert.Equal(t, "clustarr-manager-1", *got.Spec.HolderIdentity)
	assert.EqualValues(t, 30, *got.Spec.LeaseDurationSeconds)
	assert.True(t, got.Spec.RenewTime.Time.Equal(now), "renewed now")

	now = now.Add(time.Second)
	l.Set(t.Context(), 3)
	assert.True(t, readLease(t, c, l.Key).Spec.RenewTime.Time.Equal(now.Add(-time.Second)), "an unchanged count writes nothing")

	l.Set(t.Context(), 5)
	got = readLease(t, c, l.Key)
	assert.Equal(t, "5", got.Annotations[IntelWorkAnnotation])
	assert.True(t, got.Spec.RenewTime.Time.Equal(now))
}

func TestZeroIsPublishedAtOnce(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	now := time.Date(2026, 11, 10, 23, 0, 0, 0, time.UTC)
	l := intelWorkLease(t, c, "clustarr-manager-1", &now)
	l.Set(t.Context(), 3)
	now = now.Add(2 * time.Second)
	l.Set(t.Context(), 0)
	got := readLease(t, c, l.Key)
	assert.Equal(t, "0", got.Annotations[IntelWorkAnnotation])
	assert.True(t, got.Spec.RenewTime.Time.Equal(now), "zero is written the moment it is known")
}

func TestANewHolderReplacesTheOld(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	now := time.Date(2026, 11, 10, 23, 0, 0, 0, time.UTC)
	intelWorkLease(t, c, "clustarr-manager-1", &now).Set(t.Context(), 3)
	intelWorkLease(t, c, "clustarr-manager-2", &now).Set(t.Context(), 2)
	got := readLease(t, c, types.NamespacedName{Namespace: "clustarr-system", Name: "clustarr-intel-work"})
	assert.Equal(t, "2", got.Annotations[IntelWorkAnnotation], "the new leader's count, not a sum")
	assert.Equal(t, "clustarr-manager-2", *got.Spec.HolderIdentity)
}

func TestAFailedPublishIsLoggedNotReturned(t *testing.T) {
	fail := true
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if fail {
				return errors.New("leases is forbidden")
			}
			return cl.Create(ctx, obj, opts...)
		},
	}).Build()
	now := time.Date(2026, 11, 10, 23, 0, 0, 0, time.UTC)
	l := intelWorkLease(t, c, "clustarr-manager-1", &now)
	// The warning goes to the logger the context carries (the manager's,
	// in its format and at its level), never a default or a field's own.
	var logged bytes.Buffer
	ctx := logging.NewContext(t.Context(), slog.New(slog.NewTextHandler(&logged, nil)))
	l.Set(ctx, 4) // refused: logged, kept
	var missing coordinationv1.Lease
	require.Error(t, c.Get(t.Context(), l.Key, &missing))
	assert.Contains(t, logged.String(), "cannot publish the intel-work Lease")
	assert.Contains(t, logged.String(), "leases is forbidden")
	fail = false
	require.NoError(t, l.Publish(t.Context()), "the kept value is published on the next try")
	assert.Equal(t, "4", readLease(t, c, l.Key).Annotations[IntelWorkAnnotation])
}

func TestASetDuringARenewalIsNeverOverwrittenByIt(t *testing.T) {
	// A renewal in flight with the old count must not land after a Set
	// that moved it (the zero the pass publishes at once): the renewal's
	// retry from a fresh read would otherwise resurrect the old count with
	// a fresh renew time, and cluster-plex would read work for 30 s more.
	first, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithInterceptorFuncs(interceptor.Funcs{
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if obj.GetAnnotations()[IntelWorkAnnotation] == "3" {
				once.Do(func() { close(first); <-release })
			}
			return cl.Update(ctx, obj, opts...)
		},
	}).Build()
	now := time.Date(2026, 11, 10, 23, 0, 0, 0, time.UTC)
	l := intelWorkLease(t, c, "clustarr-manager-1", &now)
	l.Set(t.Context(), 3) // the Lease exists at 3

	renewed := make(chan error, 1)
	go func() { renewed <- l.Publish(t.Context()) }() // the renewer's tick, held in its Update
	<-first
	set := make(chan struct{})
	go func() { l.Set(t.Context(), 0); close(set) }() // the pass moves the count meanwhile
	select {
	case <-set:
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-renewed)
	<-set
	assert.Equal(t, "0", readLease(t, c, l.Key).Annotations[IntelWorkAnnotation], "the count set last is the count published")
}

func TestTheRenewerRenewsWhileWorkIsHeld(t *testing.T) {
	var writes atomic.Int32
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithInterceptorFuncs(interceptor.Funcs{
		Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			writes.Add(1)
			return cl.Update(ctx, obj, opts...)
		},
	}).Build()
	now := time.Date(2026, 11, 10, 23, 0, 0, 0, time.UTC)
	l := intelWorkLease(t, c, "clustarr-manager-1", &now)
	l.Every = 20 * time.Millisecond
	assert.True(t, l.NeedLeaderElection(), "only the leader publishes")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = l.Start(ctx) }()
	l.Set(ctx, 1)
	time.Sleep(90 * time.Millisecond)
	assert.GreaterOrEqual(t, writes.Load(), int32(3), "renewed about every Every")
	l.Set(ctx, 0)
	n := writes.Load()
	time.Sleep(60 * time.Millisecond)
	assert.Equal(t, n, writes.Load(), "nothing renews at zero")
}

func TestAReconcilePublishesItsIntelWork(t *testing.T) {
	(&Reconciler{}).publishIntelWork(t.Context(), PassResult{IntelWork: 2}) // none configured: nothing, no panic
	c := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	now := time.Date(2026, 11, 10, 23, 0, 0, 0, time.UTC)
	r := &Reconciler{IntelWork: intelWorkLease(t, c, "clustarr-manager-1", &now)}
	r.publishIntelWork(t.Context(), PassResult{IntelWork: 2})
	assert.Equal(t, "2", readLease(t, c, r.IntelWork.Key).Annotations[IntelWorkAnnotation])
}
