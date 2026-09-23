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
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/history"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// fakeDLQ is a DLQReader over a fixed set of dead letters.
type fakeDLQ struct {
	bySeq  map[uint64]*events.Envelope
	subj   map[uint64]string
	lastOn map[string]uint64
}

func (f *fakeDLQ) GetDeadLetter(_ context.Context, seq uint64) (string, *events.Envelope, error) {
	env, ok := f.bySeq[seq]
	if !ok {
		return "", nil, history.ErrDeadLetterNotFound
	}
	return f.subj[seq], env.Clone(), nil
}

func (f *fakeDLQ) LastDeadLetterSeq(_ context.Context, subject string) (uint64, error) {
	seq, ok := f.lastOn[subject]
	if !ok {
		return 0, history.ErrDeadLetterNotFound
	}
	return seq, nil
}

// deadLetterFor builds the envelope events.DeadLetter would store for a
// SearchTask about movie name in ns.
func deadLetterFor(t *testing.T, ns, name string) (dlqSubject, workSubject string, env *events.Envelope) {
	t.Helper()
	orig := envelopeFor(t, ns+"/"+name, schema.SearchTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name},
		Reason:   schema.SearchReasonMissing,
	})
	orig.ID = "search:" + name
	workSubject = "clustarr.work.catalogarr.search.high." + name
	dlqSubject, env = events.DeadLetter(
		deadLetterMessage{testMessage: testMessage{env: orig, subject: workSubject}, attempt: 5},
		"catalogarr-search-high", "max deliveries exceeded (5): indexer timeout")
	return dlqSubject, workSubject, env
}

// published is one call a recordingBus saw.
type published struct {
	subject string
	env     *events.Envelope
}

type recordingBus struct {
	mu   sync.Mutex
	msgs []published
}

func (b *recordingBus) Publish(_ context.Context, subject string, e *events.Envelope, _ ...events.PublishOption) (events.Receipt, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.msgs = append(b.msgs, published{subject: subject, env: e.Clone()})
	return events.Receipt{}, nil
}

func (b *recordingBus) snapshot() []published {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]published(nil), b.msgs...)
}

func (r *fakeRecorder) reasons() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.events))
	for _, e := range r.events {
		out = append(out, e.reason)
	}
	return out
}

func annotate(t *testing.T, ctx context.Context, c client.Client, m *catalogv1alpha1.Movie, key, value string) {
	t.Helper()
	var live catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(m), &live))
	patch := client.MergeFrom(live.DeepCopy())
	if live.Annotations == nil {
		live.Annotations = map[string]string{}
	}
	live.Annotations[key] = value
	require.NoError(t, c.Patch(ctx, &live, patch))
}

// TestReplayer is the clustarr.io/replay handler end to end against a real
// apiserver: the DLQ projector marks a Movie (recording the dead letter's
// sequence), an operator annotates the sequence, and the replay handler's
// metadata-only watch republishes the message to its original subject and
// consumes the annotations. Then every request it must refuse.
func TestReplayer(t *testing.T) {
	c := requireTestClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ns := newNamespace(t, ctx, c)

	heatDLQ, heatWork, heatDead := deadLetterFor(t, ns, "heat")
	_, _, otherDead := deadLetterFor(t, ns, "somebody-else")
	dlq := &fakeDLQ{
		bySeq:  map[uint64]*events.Envelope{7: heatDead, 8: otherDead},
		subj:   map[uint64]string{7: heatDLQ, 8: "clustarr.dlq.catalogarr.search.somebody-else"},
		lastOn: map[string]uint64{heatDLQ: 7},
	}

	mgr, err := ctrl.NewManager(testCfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)
	bus := &recordingBus{}
	rec := &fakeRecorder{}
	require.NoError(t, history.NewReplayer(history.ReplayDeps{
		Client: mgr.GetClient(), Bus: bus, DLQ: dlq, Recorder: rec,
	}).SetupWithManager(mgr))
	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))

	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: ns},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "hd", RootFolderRef: "movies", Monitored: ptr.To(true)},
	}
	require.NoError(t, c.Create(ctx, movie))

	// The projector marks it, naming the sequence through the DLQ reader.
	proj := history.NewDLQProjector(history.DLQDeps{Client: c, Recorder: rec, DLQ: dlq})
	require.NoError(t, proj.Handle(ctx, testMessage{env: heatDead, subject: heatDLQ}))
	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	require.Equal(t, "7", got.Annotations[history.AnnotationDeadLetterSeq], "the projector records the sequence to replay")
	require.Contains(t, got.Annotations[history.AnnotationDeadLettered], heatWork+"@")
	assertProjectorOwnsExactly(t, &got, history.AnnotationDeadLettered, history.AnnotationDeadLetterSeq)

	t.Run("a valid request republishes to the original subject and consumes the annotations", func(t *testing.T) {
		annotate(t, ctx, c, movie, history.AnnotationReplay, "7")
		require.Eventually(t, func() bool {
			return c.Get(ctx, client.ObjectKeyFromObject(movie), &got) == nil &&
				got.Annotations[history.AnnotationReplay] == ""
		}, 10*time.Second, 50*time.Millisecond, "the replay annotation must be consumed")

		msgs := bus.snapshot()
		require.Len(t, msgs, 1)
		assert.Equal(t, heatWork, msgs[0].subject, "a replay goes back to the subject the message dead-lettered from")
		assert.Equal(t, "replay:7:"+string(got.UID), msgs[0].env.ID, "a fresh Msg-Id, or JetStream would drop it as the original")
		assert.Equal(t, heatDead.Schema, msgs[0].env.Schema)
		assert.JSONEq(t, string(heatDead.Data), string(msgs[0].env.Data))
		for _, h := range []string{events.HeaderDLQSubject, events.HeaderDLQReason, events.HeaderDLQConsumer, events.HeaderDLQAttempts, events.HeaderDLQMsgID} {
			assert.Empty(t, msgs[0].env.Header(h), "a replay does not carry %s", h)
		}
		assert.NotContains(t, got.Annotations, history.AnnotationDeadLettered, "the replayed dead letter's marker is cleared")
		assert.NotContains(t, got.Annotations, history.AnnotationDeadLetterSeq)
		assert.Contains(t, rec.reasons(), "Replayed")
	})

	refused := func(t *testing.T, value string) {
		t.Helper()
		before := len(bus.snapshot())
		annotate(t, ctx, c, movie, history.AnnotationReplay, value)
		require.Eventually(t, func() bool {
			return c.Get(ctx, client.ObjectKeyFromObject(movie), &got) == nil &&
				got.Annotations[history.AnnotationReplay] == ""
		}, 10*time.Second, 50*time.Millisecond, "a request that can never succeed is consumed, not retried forever")
		assert.Len(t, bus.snapshot(), before, "a refused request publishes nothing")
		assert.Contains(t, rec.reasons(), "ReplayRefused")
	}

	t.Run("not a sequence number", func(t *testing.T) { refused(t, "yesterday") })
	t.Run("a sequence the DLQ does not hold", func(t *testing.T) { refused(t, "99") })
	t.Run("another object's dead letter", func(t *testing.T) {
		annotate(t, ctx, c, movie, history.AnnotationDeadLettered, "clustarr.work.x@2026-09-23T12:00:00Z")
		refused(t, "8")
		assert.Contains(t, got.Annotations, history.AnnotationDeadLettered, "a refused replay leaves the marker alone")
	})
}

// assertProjectorOwnsExactly asserts the DLQ projector's managedFields entry
// claims exactly the named annotation keys and nothing else -- the one place
// an over-claim would show.
func assertProjectorOwnsExactly(t *testing.T, obj client.Object, keys ...string) {
	t.Helper()
	for _, e := range obj.GetManagedFields() {
		if e.Manager != k8s.ManagerDLQProjector.String() {
			continue
		}
		require.Empty(t, e.Subresource, "the projector never writes a subresource")
		var fields struct {
			Metadata struct {
				Annotations map[string]json.RawMessage `json:"f:annotations"`
			} `json:"f:metadata"`
		}
		require.NoError(t, json.Unmarshal(e.FieldsV1.GetRawBytes(), &fields))
		var got []string
		for k := range fields.Metadata.Annotations {
			got = append(got, strings.TrimPrefix(k, "f:"))
		}
		assert.ElementsMatch(t, keys, got)
		return
	}
	t.Fatal("no managedFields entry for the DLQ projector")
}

// TestDLQProjector_NamespaceEventLandsInItsNamespace sends the unresolvable
// dead letter's Event through a REAL events.k8s.io broadcaster -- the fake
// recorder cannot show where an Event is filed -- and finds it in the
// namespace the dead letter concerns. It used to regard a bare Namespace
// object, which is cluster-scoped, so the recorder filed it under default.
func TestDLQProjector_NamespaceEventLandsInItsNamespace(t *testing.T) {
	c := requireTestClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ns := newNamespace(t, ctx, c)

	cs, err := kubernetes.NewForConfig(testCfg)
	require.NoError(t, err)
	broadcaster := k8sevents.NewBroadcaster(&k8sevents.EventSinkImpl{Interface: cs.EventsV1()})
	broadcaster.StartRecordingToSink(ctx.Done())
	defer broadcaster.Shutdown()
	rec := broadcaster.NewRecorder(k8s.MustNewScheme(), "clustarr-dlq-projector")

	proj := history.NewDLQProjector(history.DLQDeps{Client: c, Recorder: rec})
	env := envelopeFor(t, "", schema.WantedScan{Namespace: ns})
	env.Headers = map[string]string{
		events.HeaderDLQSubject:  "clustarr.work.catalogarr.wantedscan.low." + ns,
		events.HeaderDLQReason:   "max deliveries exceeded",
		events.HeaderDLQConsumer: "catalogarr-search-normal",
		events.HeaderDLQAttempts: "5",
	}
	require.NoError(t, proj.Handle(ctx, testMessage{env: env, subject: "clustarr.dlq.catalogarr.wantedscan." + ns}))

	find := func(inNamespace string) (*eventsv1.Event, error) {
		var list eventsv1.EventList
		if err := c.List(ctx, &list, client.InNamespace(inNamespace)); err != nil {
			return nil, err
		}
		for i := range list.Items {
			e := &list.Items[i]
			if e.Reason == "DeadLettered" && e.Regarding.Kind == "Namespace" && e.Regarding.Name == ns {
				return e, nil
			}
		}
		return nil, errors.New("not found")
	}
	var got *eventsv1.Event
	require.Eventually(t, func() bool {
		got, err = find(ns)
		return err == nil
	}, 10*time.Second, 100*time.Millisecond, "the Event must be filed in the namespace the dead letter concerns")
	assert.Equal(t, corev1.EventTypeWarning, got.Type)
	_, err = find(metav1.NamespaceDefault)
	assert.Error(t, err, "and not in default")
}
