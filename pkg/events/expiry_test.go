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

package events_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/events"
)

// TestAutoscaledDurablesNeverExpire: no consumer may set an inactivity
// threshold. A durable must outlive zero replicas, or its metric becomes
// ErrConsumerNotFound for good and its domain can never wake (split §9.8 as
// amended 2026-10-07).
func TestAutoscaledDurablesNeverExpire(t *testing.T) {
	if _, ok := reflect.TypeOf(events.ConsumerSpec{}).FieldByName("InactiveThreshold"); ok {
		t.Fatal("ConsumerSpec grew an InactiveThreshold field")
	}
	if _, ok := reflect.TypeOf(events.Subscription{}).FieldByName("InactiveThreshold"); ok {
		t.Fatal("Subscription grew an InactiveThreshold field")
	}
	consumers := append([]events.ConsumerSpec{
		events.TranscodeTaskConsumer("6f1c-uid", "cpu"),
		events.EngineConsumer("qbit", 0),
	}, events.Default().Consumers...)
	for _, c := range consumers {
		assert.Zero(t, events.ConsumerConfig(c).InactiveThreshold, c.Name)
		assert.Zero(t, events.ConsumerConfig(events.SubscriptionSpec(c.Subscription())).InactiveThreshold,
			"%s: as Pull creates it", c.Name)
	}
}
