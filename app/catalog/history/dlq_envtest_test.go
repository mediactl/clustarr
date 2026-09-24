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

package history_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/history"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestDLQProjector_AnnotatesExactlyOneLeaf_NeverStatus is ruling R1's central
// claim made falsifiable: a server-side-apply of one annotation key owns
// exactly that leaf, and the manager that applies it never appears on the
// status subresource. Both halves are asserted against a real apiserver's
// metadata.managedFields, not inferred from this package's own code -- an
// over-claim is otherwise completely silent (CLAUDE.md, "Server-side apply").
//
// The Movie is created with real status already set, under
// k8s.ManagerCatalogarr, before DLQProjector.Handle ever runs: a blank
// object has no status to release, so a test against one cannot observe a
// release even if the code had one (CLAUDE.md again, same section).
func TestDLQProjector_AnnotatesExactlyOneLeaf_NeverStatus(t *testing.T) {
	c := requireTestClient(t)
	ctx := context.Background()
	ns := newNamespace(t, ctx, c)

	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "the-matrix", Namespace: ns},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID: 603, QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies",
			Monitored: ptr.To(true),
		},
	}
	require.NoError(t, c.Create(ctx, movie))

	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr,
		catalogac.Movie(movie.Name, ns).WithStatus(
			catalogac.MovieStatus().WithPhase(catalogv1alpha1.MoviePhaseWanted).WithAvailable(true),
		))
	require.NoError(t, err)

	var before catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &before))
	require.Equal(t, catalogv1alpha1.MoviePhaseWanted, before.Status.Phase, "fixture sanity: status must be set before Handle runs")

	rec := &fakeRecorder{}
	fixedNow := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	proj := history.NewDLQProjector(history.DLQDeps{
		Client:   c,
		Recorder: rec,
		Now:      func() time.Time { return fixedNow },
	})

	origSubject := "clustarr.work.catalogarr.search.high." + ns + "/the-matrix"
	env := envelopeFor(t, ns+"/the-matrix", schema.SearchTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:   schema.SearchReasonMissing,
	})
	env.Headers = map[string]string{
		events.HeaderDLQSubject:  origSubject,
		events.HeaderDLQReason:   "max deliveries exceeded (5): indexer timeout",
		events.HeaderDLQConsumer: "catalogarr-search-high",
		events.HeaderDLQAttempts: "5",
	}
	msg := testMessage{env: env, subject: "clustarr.dlq.catalogarr.search." + ns + "/the-matrix"}

	require.NoError(t, proj.Handle(ctx, msg))

	var after catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &after))

	// The value: "<original subject>@<RFC3339>".
	require.Equal(t, origSubject+"@2026-09-23T12:00:00Z",
		after.Annotations[history.AnnotationDeadLettered])

	// Status is byte-for-byte unchanged: the projector's apply body never
	// mentioned it, so there was nothing to release even in principle, but
	// this is the direct, value-level half of the proof -- not just an
	// absence in managedFields.
	require.Equal(t, before.Status, after.Status)

	// Exactly one managedFields entry for this manager, and it is on the
	// MAIN resource, never on "status".
	var mainEntry *metav1.ManagedFieldsEntry
	for i := range after.ManagedFields {
		e := &after.ManagedFields[i]
		if e.Manager != k8s.ManagerDLQProjector.String() {
			continue
		}
		require.NotEqual(t, "status", e.Subresource,
			"clustarr-dlq-projector must never write the status subresource")
		require.Nil(t, mainEntry, "expected exactly one managedFields entry for this manager")
		mainEntry = e
	}
	require.NotNil(t, mainEntry, "clustarr-dlq-projector did not appear in managedFields at all")

	// The entry's FieldsV1 must declare exactly one leaf: metadata.
	// annotations.<one key>. Not the whole annotations map (which would
	// claim every OTHER manager's annotations too under force-ownership),
	// and nothing under spec or status.
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(mainEntry.FieldsV1.GetRawBytes(), &fields))
	require.Len(t, fields, 1, "must declare exactly one top-level field")
	require.Contains(t, fields, "f:metadata")
	_, hasSpec := fields["f:spec"]
	_, hasStatus := fields["f:status"]
	require.False(t, hasSpec, "must never claim spec")
	require.False(t, hasStatus, "must never claim status")

	var metaFields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(fields["f:metadata"], &metaFields))
	require.Len(t, metaFields, 1, "must declare exactly one field under metadata")
	require.Contains(t, metaFields, "f:annotations")

	var annoFields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(metaFields["f:annotations"], &annoFields))
	require.Len(t, annoFields, 1, "must own exactly one annotation key, not the whole annotations map")

	// The Event: Warning, reason DeadLettered, regarding the same Movie.
	require.Len(t, rec.events, 1)
	got := rec.events[0]
	require.Equal(t, corev1.EventTypeWarning, got.eventtype)
	require.Equal(t, "DeadLettered", got.reason)
	require.Equal(t, "Movie", got.regarding.GetObjectKind().GroupVersionKind().Kind)
}

// TestDLQProjector_UnresolvableKind_NoAnnotation_NamespaceEvent proves the
// "never guess" half of ruling R1 and the task brief: a dead letter whose
// object kind cannot be established unambiguously gets a namespace-level
// Event and nothing else -- no annotation is applied to anything, because
// there is no correctly-typed object to apply it to.
func TestDLQProjector_UnresolvableKind_NoAnnotation_NamespaceEvent(t *testing.T) {
	c := requireTestClient(t)
	ctx := context.Background()
	ns := newNamespace(t, ctx, c)

	// A real, unrelated Movie sits in the same namespace so the test can
	// prove Handle touched NOTHING, not merely that it avoided a specific
	// object it was never told about.
	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "bystander", Namespace: ns},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID: 1, QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies",
			Monitored: ptr.To(true),
		},
	}
	require.NoError(t, c.Create(ctx, movie))

	rec := &fakeRecorder{}
	proj := history.NewDLQProjector(history.DLQDeps{Client: c, Recorder: rec})

	// WantedScan names a namespace sweep, never one object -- see
	// target.go's resolveWantedScan.
	env := envelopeFor(t, "", schema.WantedScan{Namespace: ns})
	env.Headers = map[string]string{
		events.HeaderDLQSubject:  "clustarr.work.catalogarr.wantedscan.low." + ns,
		events.HeaderDLQReason:   "max deliveries exceeded",
		events.HeaderDLQConsumer: "catalogarr-search-normal",
		events.HeaderDLQAttempts: "5",
	}
	msg := testMessage{env: env, subject: "clustarr.dlq.catalogarr.wantedscan." + ns}

	require.NoError(t, proj.Handle(ctx, msg))

	require.Len(t, rec.events, 1)
	got := rec.events[0]
	require.Equal(t, corev1.EventTypeWarning, got.eventtype)
	require.Equal(t, "Namespace", got.regarding.GetObjectKind().GroupVersionKind().Kind)
	require.Equal(t, ns, got.regarding.(*corev1.Namespace).Name)

	// The bystander Movie must be completely untouched: no annotation, and
	// no managedFields entry for this manager at all -- proof that the
	// unresolvable path never calls Patch on anything, not just that it
	// happens to skip the one object a narrower test might have set up.
	var after catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &after))
	require.NotContains(t, after.Annotations, history.AnnotationDeadLettered)
	for _, e := range after.ManagedFields {
		require.NotEqual(t, k8s.ManagerDLQProjector.String(), e.Manager)
	}
}
