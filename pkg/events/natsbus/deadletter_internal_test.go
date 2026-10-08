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

package natsbus

import (
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"

	"github.com/mediactl/clustarr/pkg/events"
)

// TestMaxDeliveriesAdvisoryMatchesServer holds the restated subject prefix
// (events.SubjectMaxDeliveriesAdvisoryPrefix, which the advisory stream
// captures), the watcher's schema type and its JSON field names to
// nats-server's own, which the production binary deliberately does not
// import. A drift would leave the stream capturing, and the watcher reading, a
// subject nothing publishes on, or decoding a zero stream sequence, and every
// hung handler's message would be dropped again with no test noticing.
func TestMaxDeliveriesAdvisoryMatchesServer(t *testing.T) {
	if events.SubjectMaxDeliveriesAdvisoryPrefix != natsserver.JSAdvisoryConsumerMaxDeliveryExceedPre {
		t.Errorf("advisory prefix = %q, nats-server publishes on %q",
			events.SubjectMaxDeliveriesAdvisoryPrefix, natsserver.JSAdvisoryConsumerMaxDeliveryExceedPre)
	}
	if want := natsserver.JSAdvisoryConsumerMaxDeliveryExceedPre + ".>"; events.FilterMaxDeliveriesAdvisories != want {
		t.Errorf("advisory stream filter = %q, want %q", events.FilterMaxDeliveriesAdvisories, want)
	}
	if maxDeliveriesAdvisoryType != natsserver.JSConsumerDeliveryExceededAdvisoryType {
		t.Errorf("advisory type = %q, nats-server sends %q",
			maxDeliveriesAdvisoryType, natsserver.JSConsumerDeliveryExceededAdvisoryType)
	}

	sent := natsserver.JSConsumerDeliveryExceededAdvisory{
		TypedEvent: natsserver.TypedEvent{
			Type: natsserver.JSConsumerDeliveryExceededAdvisoryType,
			ID:   "adv-1",
			Time: time.Now().UTC(),
		},
		Stream:     "CLUSTARR_WORK_INDEX",
		Consumer:   "index-rss",
		StreamSeq:  42,
		Deliveries: 4,
	}
	data, err := json.Marshal(sent)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got maxDeliveriesAdvisory
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	want := maxDeliveriesAdvisory{
		Type:       sent.Type,
		Stream:     sent.Stream,
		Consumer:   sent.Consumer,
		StreamSeq:  sent.StreamSeq,
		Deliveries: sent.Deliveries,
	}
	if got != want {
		t.Errorf("decoded %+v from %s, want %+v", got, data, want)
	}
	if subj := events.MaxDeliveriesAdvisorySubject(sent.Stream, sent.Consumer); subj !=
		natsserver.JSAdvisoryConsumerMaxDeliveryExceedPre+".CLUSTARR_WORK_INDEX.index-rss" {
		t.Errorf("advisory subject = %q", subj)
	}
}

// TestWatcherAckWaitCoversItsBatch: the watcher's AckWait covers the
// MaxAckPending advisories one pull holds, each allowed deadLetterTimeout and
// handled one at a time, twice over (spec §5.9 keeps the tuning), and one
// watchOnce fetches no more than that batch.
func TestWatcherAckWaitCoversItsBatch(t *testing.T) {
	want := 2 * time.Duration(events.DeadLetterWatcherMaxAckPending) * deadLetterTimeout
	if events.DeadLetterWatcherAckWait != want {
		t.Errorf("DeadLetterWatcherAckWait = %v, want %v", events.DeadLetterWatcherAckWait, want)
	}
	w := events.DeadLetterWatcherSpec(events.ConsumerSpec{
		Name: "index-rss", Stream: events.StreamWorkIndex, Filters: []string{events.FilterIndexRSS},
	})
	if w.AckWait != events.DeadLetterWatcherAckWait || w.MaxAckPending != events.DeadLetterWatcherMaxAckPending {
		t.Errorf("watcher spec AckWait %v MaxAckPending %d, want %v and %d",
			w.AckWait, w.MaxAckPending, events.DeadLetterWatcherAckWait, events.DeadLetterWatcherMaxAckPending)
	}
}
