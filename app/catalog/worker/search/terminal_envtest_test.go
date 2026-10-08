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
	"github.com/mediactl/clustarr/app/catalog/searchoutcome"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// TestWorkerReportsATerminalFailureOnTheSearchObject pins the fix for a
// misleading status: a config error the worker can see -- here, a Movie with
// no qualityProfileRef -- used to be dead-lettered in silence, leaving the
// Search in Running until the reconciler's five-minute timeout reported
// "Timeout: no results after 5m0s". Since A4.3 the worker answers it in the
// Search's record, failed, with its own outcome saying why; the Search
// controller projects the outcomes (searchoutcome.Map) into status.
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
		SearchRef:   &schema.Ref{Namespace: f.ns, Name: "srch-terminal", UID: string(srch.UID)},
		UserInvoked: true,
		Seq:         1,
	})})

	require.NoError(t, err, "a config error is answered, failed, so the dispatch closes")

	rec := f.record(t, agentrecords.SearchKey(string(srch.UID)))
	require.Equal(t, records.StateFailed, rec.State)
	require.False(t, rec.FinishedAt.IsZero(), "finishedAt is the controller's completion trigger")
	require.Empty(t, rec.Candidates)
	outs := searchoutcome.Map(rec.IndexerOutcomes)
	require.Len(t, outs, 1)
	require.Equal(t, searchoutcome.WorkerOutcomeName, outs[0].Name)
	require.Equal(t, catalogv1alpha1.IndexerOutcomeError, outs[0].State)
	require.Contains(t, outs[0].Error, "qualityProfileRef")

	got := &catalogv1alpha1.Search{}
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "srch-terminal"}, got))
	require.Empty(t, string(got.Status.Phase), "the worker writes no status")
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
		SearchRef:   &schema.Ref{Namespace: f.ns, Name: "srch-kind", UID: string(srch.UID)},
		UserInvoked: true,
		Seq:         1,
	})})
	require.NoError(t, err, "answered, failed")

	rec := f.record(t, agentrecords.SearchKey(string(srch.UID)))
	require.Equal(t, records.StateFailed, rec.State)
	require.False(t, rec.FinishedAt.IsZero())
	require.Len(t, rec.IndexerOutcomes, 1)
	require.Contains(t, rec.IndexerOutcomes[0].Error, "is not searchable")
}

// TestWorkerCapsAndDedupesIndexerOutcomes proves the status survives a
// pathological reply. status.indexerOutcomes is listType=map keyed by name
// with MaxItems=100, so a duplicate name or a 101st entry makes the apiserver
// reject the WHOLE apply -- taking the results with it and stranding the
// Search in Running until the reconciler's timeout. The worker caps the
// record; the Search controller's projection (searchoutcome.Map) names,
// dedupes and caps what it applies.
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
		SearchRef:   &schema.Ref{Namespace: f.ns, Name: "srch-outcomes", UID: string(srch.UID)},
		UserInvoked: true,
		Seq:         1,
	})}), "the record takes an over-long reply")

	rec := f.record(t, agentrecords.SearchKey(string(srch.UID)))
	require.LessOrEqual(t, len(rec.IndexerOutcomes), schema.MaxIndexerOutcomes)
	require.Len(t, rec.Candidates, 1, "the results survived alongside the truncated outcomes")
	outs := searchoutcome.Map(rec.IndexerOutcomes)
	require.LessOrEqual(t, len(outs), 100)
	require.NotEmpty(t, outs)

	seen := map[string]struct{}{}
	for _, o := range outs {
		require.NotEmpty(t, o.Name, "a nameless entry would break the listMapKey")
		_, dup := seen[o.Name]
		require.False(t, dup, "duplicate outcome name %q", o.Name)
		seen[o.Name] = struct{}{}
	}
	require.Equal(t, catalogv1alpha1.IndexerOutcomeOK, outs[0].State,
		"first entry wins on a duplicate name")
}

// TestWorkerReportsNamelessOutcomesAndTruncation pins two carried D1 defects:
// an outcome index reported with no indexer name was dropped, so an
// indexer that failed before it was named left no trace an operator could
// see; and a truncated reply was only a boolean on an Info log line, so a
// user reading status.results could not tell they were decided from a
// partial set. The worker's record keeps both; the Search controller's
// projection (searchoutcome.Map) names them.
func TestWorkerReportsNamelessOutcomesAndTruncation(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-nameless")

	f.rpc.Response = schema.SearchResponse{
		Releases: []schema.Release{rpcRelease("g1", "The.Matrix.1999.1080p.BluRay.x264-HIGH", 100)},
		Outcomes: []schema.SearchOutcome{
			{IndexerRef: schema.Ref{Namespace: f.ns, Name: "idx"}, Status: schema.SearchOutcomeOK, Releases: 1},
			{Status: schema.SearchOutcomeError, Error: "tls: handshake failure"},
			{Status: schema.SearchOutcomeTimeout},
		},
		Truncated: true,
	}
	srch := &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "srch-nameless", Namespace: f.ns},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
			TTL:      metav1.Duration{Duration: time.Hour},
		},
	}
	require.NoError(t, f.mgr.Create(ctx, srch))
	waitCached(t, ctx, f.mgr, client.ObjectKey{Namespace: f.ns, Name: "srch-nameless"}, &catalogv1alpha1.Search{})

	require.NoError(t, f.worker.Handle(ctx, testMessage{env: f.envelope(t, schema.SearchTask{
		MediaRef:    commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:      schema.SearchReasonInteractive,
		SearchRef:   &schema.Ref{Namespace: f.ns, Name: "srch-nameless", UID: string(srch.UID)},
		UserInvoked: true,
		Seq:         1,
	})}))

	rec := f.record(t, agentrecords.SearchKey(string(srch.UID)))
	outs := searchoutcome.Map(rec.IndexerOutcomes)
	byName := map[string]catalogv1alpha1.IndexerOutcome{}
	for _, o := range outs {
		byName[o.Name] = o
	}
	require.Len(t, byName, 4, "one named indexer, two nameless ones and the truncation marker: %+v", outs)
	require.Equal(t, "idx", outs[0].Name, "real outcomes keep the reply's order, first")

	first, ok := byName[searchoutcome.UnnamedOutcomeName(1)]
	require.True(t, ok, "the first nameless outcome is reported, not dropped")
	require.Equal(t, catalogv1alpha1.IndexerOutcomeError, first.State)
	require.Equal(t, "tls: handshake failure", first.Error, "its error is what the operator needs")
	second, ok := byName[searchoutcome.UnnamedOutcomeName(2)]
	require.True(t, ok)
	require.Equal(t, catalogv1alpha1.IndexerOutcomeTimeout, second.State)

	marker, ok := byName[searchoutcome.TruncatedOutcomeName]
	require.True(t, ok, "a truncated reply is visible on the object")
	require.Equal(t, catalogv1alpha1.IndexerOutcomeSkipped, marker.State)
	require.Contains(t, marker.Error, fmt.Sprint(schema.MaxSearchReleases))
	require.Len(t, rec.Candidates, 1, "the results are written alongside")
}
