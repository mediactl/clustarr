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

package natsbus_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

// TestEveryScheduledSubjectKeepsItsID publishes WithScheduleAt on each subject
// family a production caller schedules, through its own durable from
// events.Default(), and asserts the handler sees the ID and no transport
// header (M2's sweep). A fired schedule loses Nats-Msg-Id (nats-server
// scheduler.go), so the ID rides Clustarr-Id.
//
// The callers, as built: app/indexer/rssschedule (an indexer's next RSS
// poll), app/catalog/segmentplan and the markers stage (a season's analysis
// plan, held to its 5-minute bucket), and the downloads stage's import
// inspect and execute tasks, scheduled at the pass's own now
// (app/remediation/downloads/imports.go).
func TestEveryScheduledSubjectKeepsItsID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	bus, err := natsbus.New(connect(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })
	top := events.Default().ForSingleNode()
	require.NoError(t, bus.Ensure(ctx, top))
	for _, tc := range []struct {
		name, durable, subject string
		at                     time.Duration // from now
	}{
		{"rss", events.ConsumerIndexRSS, events.WorkRSSSubject("idx-x"), time.Second},
		{"segments-plan", events.ConsumerCatalogSegmentsPlan, events.WorkSegmentsPlanSubject("series-x"), time.Second},
		{"import-inspect", events.ConsumerImportFile, events.WorkImportInspectSubject("entry-x"), 0},
		{"import-execute", events.ConsumerImportFile, events.WorkImportExecuteSubject("entry-y"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, ok := top.Consumer(tc.durable)
			require.True(t, ok, "the topology declares %s", tc.durable)
			require.True(t, events.SubjectMatches(spec.Filters[0], tc.subject), "%s is under %s's filter", tc.subject, tc.durable)
			got := make(chan *events.Envelope, 1)
			stop, err := bus.Subscribe(ctx, spec.Subscription(), func(_ context.Context, m events.Message) error {
				select {
				case got <- m.Envelope():
				default:
				}
				return nil
			})
			require.NoError(t, err)
			defer stop()
			id := "sched-" + tc.name
			_, err = bus.Publish(ctx, tc.subject,
				&events.Envelope{ID: id, Schema: "test.Task.v1", Data: []byte(`{}`)},
				events.WithMsgID(id), events.WithScheduleAt(time.Now().Add(tc.at)))
			require.NoError(t, err)
			select {
			case env := <-got:
				assert.Equal(t, id, env.ID, "the handler sees the scheduled task's ID")
				for k := range env.Headers {
					assert.False(t, strings.HasPrefix(strings.ToLower(k), "nats-"),
						"a transport header %q reached the envelope", k)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("the scheduled task was never delivered")
			}
		})
	}
}
