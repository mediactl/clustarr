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

package indexer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/ratelimit"
)

// A deletion prunes the caps memo, which is keyed by UID, and leaves the
// limiter bucket alone, which is keyed by HOST.
//
// Several Indexers behind one host is the expected topology -- a Prowlarr or
// Jackett instance in front of many trackers -- so dropping the bucket when
// one of them is deleted would leave the survivors drawing on the Limiter's
// default config, which is unlimited, until each happened to reconcile
// again.
func TestDeletionPrunesTheCapsMemoButNotTheSharedBucket(t *testing.T) {
	const host = "shared.invalid:443"
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	uid := types.UID("indexer-uid")

	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "going",
			Namespace:         "media",
			UID:               uid,
			Generation:        4,
			DeletionTimestamp: ptr.To(metav1.NewTime(now)),
			Finalizers:        []string{"test.clustarr.io/keep"},
		},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: "https://" + host,
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolTorrent},
		},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(idx).Build()

	lim := ratelimit.New(ratelimit.Config{})
	// One token per hour, burst 1: the survivor's pacing, as buildClient
	// would have configured it.
	lim.SetConfig(host, ratelimit.Config{RPS: 1.0 / 3600, Burst: 1})

	r := NewReconciler(c, nil, lim)
	r.markProbed(uid, 4, now)
	require.False(t, r.shouldProbe(uid, 4, true, now), "setup: the memo is warm")

	res, err := r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "media", Name: "going"}})
	require.NoError(t, err)
	require.Zero(t, res.RequeueAfter)

	require.True(t, r.shouldProbe(uid, 4, true, now), "the caps memo was not pruned on delete")

	require.True(t, lim.Allow(host), "the shared bucket lost its burst token")
	require.False(t, lim.Allow(host),
		"the host's bucket was removed: a surviving Indexer behind this host would now be unpaced")
}

func TestRateLimited(t *testing.T) {
	cases := []struct {
		name    string
		limits  *indexv1alpha1.Limits
		st      indexv1alpha1.IndexerStatus
		want    bool
		message string
	}{
		{name: "no limits", limits: nil},
		{
			name: "under", limits: &indexv1alpha1.Limits{QueryLimit: ptr.To(int32(100))},
			st: indexv1alpha1.IndexerStatus{QueriesInWindow: 99},
		},
		{
			name: "queries at the limit", limits: &indexv1alpha1.Limits{QueryLimit: ptr.To(int32(100))},
			st: indexv1alpha1.IndexerStatus{QueriesInWindow: 100}, want: true, message: "queries 100/100 per day",
		},
		{
			name: "grabs over the limit", limits: &indexv1alpha1.Limits{GrabLimit: ptr.To(int32(5)), Unit: indexv1alpha1.LimitUnitHour},
			st: indexv1alpha1.IndexerStatus{GrabsInWindow: 9}, want: true, message: "grabs 9/5 per hour",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, msg := rateLimited(indexv1alpha1.IndexerSpec{Limits: tc.limits}, tc.st)
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.message, msg)
		})
	}
}

func TestRequeueAfter(t *testing.T) {
	r := &Reconciler{}
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	future := metav1.NewTime(now.Add(30 * time.Minute))
	past := metav1.NewTime(now.Add(-time.Minute))

	require.Equal(t, reprobeInterval, r.requeueAfter(indexv1alpha1.IndexerStatus{}, probeOutcome{}, now))
	require.Equal(t, 90*time.Second,
		r.requeueAfter(indexv1alpha1.IndexerStatus{DisabledUntil: &future}, probeOutcome{RetryAfter: 90 * time.Second}, now),
		"the server's Retry-After outranks the local back-off")
	require.Equal(t, 30*time.Minute+time.Second,
		r.requeueAfter(indexv1alpha1.IndexerStatus{DisabledUntil: &future}, probeOutcome{}, now))
	require.Equal(t, reprobeInterval,
		r.requeueAfter(indexv1alpha1.IndexerStatus{DisabledUntil: &past}, probeOutcome{}, now))

	// Never zero: a zero RequeueAfter means "do not requeue", and the
	// RateLimited and Healthy conditions are derived from worker-owned
	// fields the watch predicate filters out, so only the tick refreshes
	// them.
	for _, st := range []indexv1alpha1.IndexerStatus{{}, {DisabledUntil: &past}, {DisabledUntil: &future}} {
		require.NotZero(t, r.requeueAfter(st, probeOutcome{}, now))
	}
}

func TestFirstNonEmpty(t *testing.T) {
	require.Equal(t, "a", firstNonEmpty("a", "b"))
	require.Equal(t, "b", firstNonEmpty("", "b"))
	require.Equal(t, "", firstNonEmpty("", ""))
}
