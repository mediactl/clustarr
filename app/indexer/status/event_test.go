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

package status

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

func TestEscalationAction(t *testing.T) {
	processStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t.Cleanup(func() { processStart = time.Now() })
	now := processStart.Add(time.Hour)
	open := metav1.NewTime(now.Add(time.Minute))
	expired := metav1.NewTime(now.Add(-time.Minute))

	healthy := indexv1alpha1.IndexerStatus{}
	disabled := indexv1alpha1.IndexerStatus{EscalationLevel: 3, DisabledUntil: &open, LastFailureAt: &expired, LastFailure: "x"}
	lapsed := indexv1alpha1.IndexerStatus{EscalationLevel: 3, DisabledUntil: &expired, LastFailureAt: &expired, LastFailure: "x"}

	tests := []struct {
		name   string
		cur    indexv1alpha1.IndexerStatus
		failed bool
		want   string
	}{
		{"a failure opens the first window", healthy, true, events.ActionDisabled},
		{"a failure after a lapsed window opens a new one", lapsed, true, events.ActionDisabled},
		{"a failure inside an open window is not re-announced", disabled, true, ""},
		{"a success that clears a window is a recovery", lapsed, false, events.ActionRecovered},
		{"a success on a healthy indexer is nothing", healthy, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var esc Escalation
			if tt.failed {
				esc = RecordFailure(tt.cur, now, "boom")
			} else {
				esc = RecordSuccess(tt.cur, now)
			}
			require.Equal(t, tt.want, EscalationAction(tt.cur, esc, tt.failed, now))
		})
	}

	// The ladder's zero first rung is not a disable: inside the startup
	// grace a failure keeps level 0, which opens no window.
	processStart = now
	esc := RecordFailure(healthy, now, "boom")
	require.Nil(t, esc.DisabledUntil)
	require.Empty(t, EscalationAction(healthy, esc, true, now))
}

func TestLimitCrossed(t *testing.T) {
	require.False(t, LimitCrossed(nil, 0, 100), "no limit is never crossed")
	require.True(t, LimitCrossed(ptr.To[int32](5), 4, 5))
	require.True(t, LimitCrossed(ptr.To[int32](5), 3, 7))
	require.False(t, LimitCrossed(ptr.To[int32](5), 5, 6), "already announced when it got there")
	require.False(t, LimitCrossed(ptr.To[int32](5), 3, 4))
}

// subscribeIndexerEvents starts the real catalogarr-history consumer from the
// shipped topology -- the sink these events exist for -- and returns what it
// has received.
func subscribeIndexerEvents(t *testing.T, bus events.Bus) func() []schema.IndexerEvent {
	t.Helper()
	spec, ok := events.Default().ForSingleNode().Consumer(events.ConsumerCatalogHistory)
	require.True(t, ok)
	var (
		mu  sync.Mutex
		got []schema.IndexerEvent
	)
	stop, err := bus.Subscribe(context.Background(), spec.Subscription(), func(_ context.Context, m events.Message) error {
		var p schema.IndexerEvent
		if err := schema.Decode(m.Envelope().Schema, m.Envelope().Data, &p); err != nil {
			return err
		}
		if want := events.IndexerEventSubject(p.Action, p.IndexerRef.UID); m.Subject() != want {
			return fmt.Errorf("event on %q, want %q", m.Subject(), want)
		}
		mu.Lock()
		defer mu.Unlock()
		got = append(got, p)
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(stop)
	return func() []schema.IndexerEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]schema.IndexerEvent(nil), got...)
	}
}

func TestPublishTransitionsReachesTheHistoryConsumer(t *testing.T) {
	// A real clock: CLUSTARR_EVENTS ages messages out, so an event stamped
	// in the distant past never reaches a consumer.
	now := time.Now().UTC().Truncate(time.Second)
	processStart = now.Add(-time.Hour)
	t.Cleanup(func() { processStart = time.Now() })

	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	t.Cleanup(func() { _ = bus.Close() })
	received := subscribeIndexerEvents(t, bus)

	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "tr", UID: "uid-tr"},
		Spec: indexv1alpha1.IndexerSpec{Limits: &indexv1alpha1.Limits{
			QueryLimit: ptr.To[int32](10), GrabLimit: ptr.To[int32](2),
		}},
	}
	prev := indexv1alpha1.IndexerStatus{QueriesInWindow: 9, GrabsInWindow: 1}
	esc := RecordFailure(prev, now, "connection refused")
	PublishTransitions(context.Background(), bus, idx, Transition{
		Prev: prev, Escalation: &esc, Failed: true,
		Queries: ptr.To[int32](10), Grabs: ptr.To[int32](2), At: now,
	})

	require.Eventually(t, func() bool { return len(received()) == 3 }, 5*time.Second, 10*time.Millisecond)
	byAction := map[string][]schema.IndexerEvent{}
	for _, e := range received() {
		require.Equal(t, "uid-tr", e.IndexerRef.UID)
		require.Equal(t, "media", e.IndexerRef.Namespace)
		byAction[e.Action] = append(byAction[e.Action], e)
	}
	require.Len(t, byAction[events.ActionDisabled], 1)
	dis := byAction[events.ActionDisabled][0]
	require.Equal(t, "connection refused", dis.Reason)
	require.Equal(t, int32(1), dis.Failures)
	require.NotNil(t, dis.Until)
	require.Equal(t, now.Add(time.Minute), dis.Until.UTC())
	require.ElementsMatch(t, []string{"queries 10/10 per day", "grabs 2/2 per day"},
		[]string{byAction[events.ActionLimited][0].Reason, byAction[events.ActionLimited][1].Reason})

	// A nil bus is a no-op, not a panic.
	PublishTransitions(context.Background(), nil, idx, Transition{Prev: prev, Escalation: &esc, Failed: true, At: now})
}
