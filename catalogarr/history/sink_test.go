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
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/history"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Sink never touches the apiserver at all -- it only decodes an envelope and
// calls Eventf -- so its tests need no envtest.

func TestSink_ItemEvent_ProjectsOntoTheCatalogItem(t *testing.T) {
	rec := &fakeRecorder{}
	sink := history.NewSink(history.SinkDeps{Recorder: rec})

	env := envelopeFor(t, "", schema.ItemEvent{
		Ref:       schema.Ref{Namespace: "default", Name: "the-matrix"},
		Media:     commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Action:    events.ActionAdded,
		Title:     "The Matrix",
		Year:      1999,
		Monitored: true,
	})
	env.Type = "catalog.ItemEvent"

	require.NoError(t, sink.Handle(context.Background(), testMessage{env: env}))

	require.Len(t, rec.events, 1)
	got := rec.events[0]
	require.Equal(t, corev1.EventTypeNormal, got.eventtype)
	require.Equal(t, "Added", got.reason)
	require.Equal(t, "catalog.ItemEvent", got.action)
	require.Contains(t, got.note, "The Matrix")

	gvk := got.regarding.GetObjectKind().GroupVersionKind()
	require.Equal(t, "Movie", gvk.Kind)
	require.Equal(t, "catalog.clustarr.io/v1alpha1", gvk.GroupVersion().String())
}

func TestSink_ReleaseEvent_RejectedIsAWarning(t *testing.T) {
	rec := &fakeRecorder{}
	sink := history.NewSink(history.SinkDeps{Recorder: rec})

	env := envelopeFor(t, "default/the-wire", schema.ReleaseEvent{
		Media:  commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: "the-wire"},
		Action: events.ActionRejected,
	})

	require.NoError(t, sink.Handle(context.Background(), testMessage{env: env}))

	require.Len(t, rec.events, 1)
	require.Equal(t, corev1.EventTypeWarning, rec.events[0].eventtype)
	require.Equal(t, "Rejected", rec.events[0].reason)
}

func TestSink_UnrecognisedSchema_SkipsWithoutAnEvent(t *testing.T) {
	rec := &fakeRecorder{}
	sink := history.NewSink(history.SinkDeps{Recorder: rec})

	env := &events.Envelope{Schema: "some.FutureEvent.v1", Key: "default/x", Data: []byte(`{}`)}

	require.NoError(t, sink.Handle(context.Background(), testMessage{env: env}))
	require.Empty(t, rec.events)
}

func TestSink_KnownSchemaButUnresolvableKind_SkipsWithoutAnEvent(t *testing.T) {
	rec := &fakeRecorder{}
	sink := history.NewSink(history.SinkDeps{Recorder: rec})

	// A recognised evt payload whose MediaKind this package does not map --
	// describeEvent succeeds, but Resolve cannot name a CR, so Handle must
	// still skip rather than emit an Event regarding nothing in particular.
	env := envelopeFor(t, "default/x", schema.ItemEvent{
		Ref:    schema.Ref{Namespace: "default", Name: "x"},
		Media:  commonv1.MediaRef{Kind: commonv1.MediaKind("podcast"), Name: "x"},
		Action: events.ActionAdded,
	})

	require.NoError(t, sink.Handle(context.Background(), testMessage{env: env}))
	require.Empty(t, rec.events)
}
