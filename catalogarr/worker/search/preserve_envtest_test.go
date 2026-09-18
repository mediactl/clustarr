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

package search_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	searchctl "github.com/mediactl/clustarr/catalogarr/controller/search"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// TestWorkerTerminalFailureKeepsAnEarlierRunsResults is the round-2
// regression test.
//
// JetStream is at-least-once. A delivery that ran successfully and then lost
// its acknowledgement is redelivered, and if that second run fails terminally
// -- here the target was deleted in between -- it must not take the first
// run's answer down with it. The user would otherwise watch results they were
// already looking at disappear from a Search that had them, with no action of
// their own.
//
// The object is driven to a real steady state through the ACTUAL write path
// first: a blank object cannot observe a release, and a hand-written status
// would not prove the worker's own code preserves anything.
func TestWorkerTerminalFailureKeepsAnEarlierRunsResults(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-preserve")

	srch := &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "srch-preserve", Namespace: f.ns},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
			TTL:      metav1.Duration{Duration: time.Hour},
		},
	}
	require.NoError(t, f.mgr.Create(ctx, srch))
	waitCached(t, ctx, f.mgr, client.ObjectKey{Namespace: f.ns, Name: "srch-preserve"}, &catalogv1alpha1.Search{})

	env := f.envelope(t, schema.SearchTask{
		MediaRef:    commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:      schema.SearchReasonInteractive,
		SearchRef:   &schema.Ref{Namespace: f.ns, Name: "srch-preserve"},
		UserInvoked: true,
	})

	// Delivery 1 succeeds. Its ack is then lost -- which the bus cannot tell
	// apart from a worker that died before acking.
	require.NoError(t, f.worker.Handle(ctx, testMessage{env: env, attempt: 1}))

	got := &catalogv1alpha1.Search{}
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "srch-preserve"}, got))
	require.Len(t, got.Status.Results, 2, "steady state: the first run found two releases")
	require.Len(t, got.Status.IndexerOutcomes, 1)
	firstFinishedAt := got.Status.FinishedAt
	require.NotNil(t, firstFinishedAt)

	// The target goes away before the redelivery lands.
	require.NoError(t, f.mgr.Delete(ctx, &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "the-matrix", Namespace: f.ns},
	}))
	eventually(t, 10*time.Second, "the cache to lose the Movie", func() bool {
		err := f.mgr.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "the-matrix"}, &catalogv1alpha1.Movie{})
		return apierrors.IsNotFound(err)
	})
	eventually(t, 10*time.Second, "the cache to see the first run's results", func() bool {
		var s catalogv1alpha1.Search
		if err := f.mgr.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "srch-preserve"}, &s); err != nil {
			return false
		}
		return len(s.Status.Results) == 2
	})

	// Delivery 2 now fails terminally.
	err := f.worker.Handle(ctx, testMessage{env: env, attempt: 2})
	var de *events.DiscardError
	require.ErrorAs(t, err, &de, "a target that is really gone is terminal for the message")

	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "srch-preserve"}, got))
	require.Len(t, got.Status.Results, 2,
		"a failed redelivery must not destroy a successful earlier answer")
	require.Equal(t, "g-high", got.Status.Results[0].GUID)
	require.Equal(t, "g-low", got.Status.Results[1].GUID)

	// The failure is reported alongside, not instead of.
	names := map[string]catalogv1alpha1.IndexerOutcome{}
	for _, o := range got.Status.IndexerOutcomes {
		names[o.Name] = o
	}
	require.Contains(t, names, searchctl.WorkerOutcomeName)
	require.Equal(t, catalogv1alpha1.IndexerOutcomeError, names[searchctl.WorkerOutcomeName].State)
	require.Contains(t, names[searchctl.WorkerOutcomeName].Error, "no longer exists")
	require.Contains(t, names, "idx", "the first run's indexer outcome is re-declared, not dropped")
	require.Equal(t, catalogv1alpha1.IndexerOutcomeOK, names["idx"].State)

	require.Empty(t, string(got.Status.Phase), "the worker still never writes a controller-owned field")
}

// TestWorkerTerminalFailureTwiceDoesNotDuplicateItsOutcome: writeFailure
// merges its entry into a list that may already contain one from a previous
// failed delivery. status.indexerOutcomes is listType=map keyed by name, so a
// duplicate would make the apiserver reject the whole apply.
func TestWorkerTerminalFailureTwiceDoesNotDuplicateItsOutcome(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-preserve-twice")

	require.NoError(t, f.mgr.Create(ctx, &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "srch-twice", Namespace: f.ns},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindArtist, Name: "aphex-twin"},
			TTL:      metav1.Duration{Duration: time.Hour},
		},
	}))
	waitCached(t, ctx, f.mgr, client.ObjectKey{Namespace: f.ns, Name: "srch-twice"}, &catalogv1alpha1.Search{})

	env := f.envelope(t, schema.SearchTask{
		MediaRef:    commonv1.MediaRef{Kind: commonv1.MediaKindArtist, Name: "aphex-twin"},
		Reason:      schema.SearchReasonInteractive,
		SearchRef:   &schema.Ref{Namespace: f.ns, Name: "srch-twice"},
		UserInvoked: true,
	})

	for attempt := uint64(1); attempt <= 2; attempt++ {
		err := f.worker.Handle(ctx, testMessage{env: env, attempt: attempt})
		var de *events.DiscardError
		require.ErrorAs(t, err, &de)
		eventually(t, 10*time.Second, "the cache to see the failure outcome", func() bool {
			var s catalogv1alpha1.Search
			if err := f.mgr.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "srch-twice"}, &s); err != nil {
				return false
			}
			return len(s.Status.IndexerOutcomes) == 1
		})
	}

	got := &catalogv1alpha1.Search{}
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "srch-twice"}, got))
	require.Len(t, got.Status.IndexerOutcomes, 1, "the fresh entry replaces the stale one rather than joining it")
	require.Equal(t, searchctl.WorkerOutcomeName, got.Status.IndexerOutcomes[0].Name)
}
