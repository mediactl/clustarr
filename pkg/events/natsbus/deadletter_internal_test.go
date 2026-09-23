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
)

// TestMaxDeliveriesAdvisoryMatchesServer holds the watcher's restated subject
// prefix, schema type and JSON field names to nats-server's own, which the
// production binary deliberately does not import. A drift would leave the
// watcher subscribed to a subject nothing publishes on, or decoding a zero
// stream sequence, and every hung handler's message would be dropped again
// with no test noticing.
func TestMaxDeliveriesAdvisoryMatchesServer(t *testing.T) {
	if maxDeliveriesAdvisoryPrefix != natsserver.JSAdvisoryConsumerMaxDeliveryExceedPre {
		t.Errorf("advisory prefix = %q, nats-server publishes on %q",
			maxDeliveriesAdvisoryPrefix, natsserver.JSAdvisoryConsumerMaxDeliveryExceedPre)
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
		Stream:     "CLUSTARR_WORK_INDEXARR",
		Consumer:   "indexarr-rss",
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
	if subj := maxDeliveriesAdvisorySubject(sent.Stream, sent.Consumer); subj !=
		natsserver.JSAdvisoryConsumerMaxDeliveryExceedPre+".CLUSTARR_WORK_INDEXARR.indexarr-rss" {
		t.Errorf("advisory subject = %q", subj)
	}
}
