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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	searchctl "github.com/mediactl/clustarr/catalogarr/controller/search"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// TestWorkerReportsATerminalFailureOnTheSearchObject pins the fix for a
// misleading status: a config error the worker can see -- here, a Movie with
// no qualityProfileRef -- used to be dead-lettered in silence, leaving the
// Search in Running until the reconciler's five-minute timeout reported
// "Timeout: no results after 5m0s". The worker owns finishedAt,
// indexerOutcomes and results, so it can say what really happened without
// touching a single controller-owned field.
func TestWorkerReportsATerminalFailureOnTheSearchObject(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-terminal")

	// A movie with no qualityProfileRef: nothing can rank its releases.
	require.NoError(t, f.mgr.Create(ctx, &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "no-profile", Namespace: f.ns},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 604, RootFolderRef: "movies"},
	}))
	srch := &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "srch-terminal", Namespace: f.ns},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "no-profile"},
			TTL:      metav1.Duration{Duration: time.Hour},
		},
	}
	require.NoError(t, f.mgr.Create(ctx, srch))
	waitCached(t, ctx, f.mgr, client.ObjectKey{Namespace: f.ns, Name: "no-profile"}, &catalogv1alpha1.Movie{})
	waitCached(t, ctx, f.mgr, client.ObjectKey{Namespace: f.ns, Name: "srch-terminal"}, &catalogv1alpha1.Search{})

	err := f.worker.Handle(ctx, testMessage{env: f.envelope(t, schema.SearchTask{
		MediaRef:    commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "no-profile"},
		Reason:      schema.SearchReasonInteractive,
		SearchRef:   &schema.Ref{Namespace: f.ns, Name: "srch-terminal"},
		UserInvoked: true,
	})})

	var de *events.DiscardError
	require.ErrorAs(t, err, &de, "a config error is still terminal for the message")

	got := &catalogv1alpha1.Search{}
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "srch-terminal"}, got))
	require.NotNil(t, got.Status.FinishedAt, "finishedAt is the reconciler's completion trigger")
	require.Empty(t, got.Status.Results)
	require.Len(t, got.Status.IndexerOutcomes, 1)
	require.Equal(t, searchctl.WorkerOutcomeName, got.Status.IndexerOutcomes[0].Name)
	require.Equal(t, catalogv1alpha1.IndexerOutcomeError, got.Status.IndexerOutcomes[0].State)
	require.Contains(t, got.Status.IndexerOutcomes[0].Error, "qualityProfileRef")
	require.Empty(t, string(got.Status.Phase), "phase belongs to the reconciler; the worker never writes it")
}

// TestWorkerReportsAnUnsupportedKindOnTheSearchObject covers the other
// terminal branch, which sits before the snapshot.
func TestWorkerReportsAnUnsupportedKindOnTheSearchObject(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-terminal-kind")

	srch := &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "srch-kind", Namespace: f.ns},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindArtist, Name: "aphex-twin"},
			TTL:      metav1.Duration{Duration: time.Hour},
		},
	}
	require.NoError(t, f.mgr.Create(ctx, srch))
	waitCached(t, ctx, f.mgr, client.ObjectKey{Namespace: f.ns, Name: "srch-kind"}, &catalogv1alpha1.Search{})

	err := f.worker.Handle(ctx, testMessage{env: f.envelope(t, schema.SearchTask{
		MediaRef:    commonv1.MediaRef{Kind: commonv1.MediaKindArtist, Name: "aphex-twin"},
		Reason:      schema.SearchReasonInteractive,
		SearchRef:   &schema.Ref{Namespace: f.ns, Name: "srch-kind"},
		UserInvoked: true,
	})})
	var de *events.DiscardError
	require.ErrorAs(t, err, &de)

	got := &catalogv1alpha1.Search{}
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "srch-kind"}, got))
	require.NotNil(t, got.Status.FinishedAt)
	require.Len(t, got.Status.IndexerOutcomes, 1)
	require.Contains(t, got.Status.IndexerOutcomes[0].Error, "M6 scope")
}

// TestWorkerCapsAndDedupesIndexerOutcomes proves the status write survives a
// pathological reply. status.indexerOutcomes is listType=map keyed by name
// with MaxItems=100, so a duplicate name or a 101st entry makes the apiserver
// reject the WHOLE apply -- taking the results with it and stranding the
// Search in Running until the reconciler's timeout.
func TestWorkerCapsAndDedupesIndexerOutcomes(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-outcomes")

	outcomes := make([]schema.SearchOutcome, 0, 260)
	for i := range 250 {
		outcomes = append(outcomes, schema.SearchOutcome{
			IndexerRef: schema.Ref{Namespace: f.ns, Name: fmt.Sprintf("idx-%03d", i)},
			Status:     schema.SearchOutcomeOK, Releases: 1,
		})
	}
	// Duplicates of an early name, and a nameless entry.
	outcomes = append(outcomes,
		schema.SearchOutcome{IndexerRef: schema.Ref{Name: "idx-000"}, Status: schema.SearchOutcomeError, Error: "second opinion"},
		schema.SearchOutcome{Status: schema.SearchOutcomeError, Error: "no name at all"},
	)
	f.rpc.Response = schema.SearchResponse{
		Releases: []schema.Release{rpcRelease("g1", "The.Matrix.1999.1080p.BluRay.x264-HIGH", 100)},
		Outcomes: outcomes,
	}

	srch := &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "srch-outcomes", Namespace: f.ns},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
			TTL:      metav1.Duration{Duration: time.Hour},
		},
	}
	require.NoError(t, f.mgr.Create(ctx, srch))
	waitCached(t, ctx, f.mgr, client.ObjectKey{Namespace: f.ns, Name: "srch-outcomes"}, &catalogv1alpha1.Search{})

	require.NoError(t, f.worker.Handle(ctx, testMessage{env: f.envelope(t, schema.SearchTask{
		MediaRef:    commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:      schema.SearchReasonInteractive,
		SearchRef:   &schema.Ref{Namespace: f.ns, Name: "srch-outcomes"},
		UserInvoked: true,
	})}), "the apiserver rejects an over-long or duplicate-keyed list outright")

	got := &catalogv1alpha1.Search{}
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "srch-outcomes"}, got))
	require.LessOrEqual(t, len(got.Status.IndexerOutcomes), 100)
	require.NotEmpty(t, got.Status.IndexerOutcomes)
	require.Len(t, got.Status.Results, 1, "the results survived alongside the truncated outcomes")

	seen := map[string]struct{}{}
	for _, o := range got.Status.IndexerOutcomes {
		require.NotEmpty(t, o.Name, "a nameless entry would break the listMapKey")
		_, dup := seen[o.Name]
		require.False(t, dup, "duplicate outcome name %q", o.Name)
		seen[o.Name] = struct{}{}
	}
	require.Equal(t, catalogv1alpha1.IndexerOutcomeOK, got.Status.IndexerOutcomes[0].State,
		"first entry wins on a duplicate name")
}
