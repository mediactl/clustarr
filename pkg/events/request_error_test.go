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
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/events"
)

// TestClassifyRequestError is ADR-0020 §4: what a Request's error says about
// the verb's responder, through any wrapping, and what each class lets its
// caller do -- send again at once only what no handler ran, retry an
// idempotent verb after a timeout too, and name the condition reason.
func TestClassifyRequestError(t *testing.T) {
	wrap := func(err error) error { return fmt.Errorf("index search: %w", err) }
	for _, tc := range []struct {
		name      string
		err       error
		want      events.RequestErrorClass
		notRun    bool
		retryable bool
		reason    string
	}{
		{"nil", nil, events.RequestOK, false, false, "RPCError"},
		{"no responders", wrap(events.ErrNoResponders), events.RequestNoResponders, true, true, "NoAgent"},
		{"busy responder", wrap(&events.ResponderError{Code: events.ServiceErrorBusy, Message: "busy"}), events.RequestBusy, true, true, "AgentBusy"},
		{"reconnect buffer full", wrap(events.ErrTransient), events.RequestBusy, true, true, "AgentBusy"},
		{"caller's deadline", wrap(context.DeadlineExceeded), events.RequestTimeout, false, true, "RPCTimeout"},
		{"nats timeout", wrap(nats.ErrTimeout), events.RequestTimeout, false, true, "RPCTimeout"},
		{"handler failed", wrap(&events.ResponderError{Code: events.ServiceErrorFailed, Message: "boom"}), events.RequestOther, false, false, "RPCError"},
		{"a foreign code reads as failed", &events.ResponderError{Code: 418, Message: "teapot"}, events.RequestOther, false, false, "RPCError"},
		{"anything else", errors.New("decode reply"), events.RequestOther, false, false, "RPCError"},
		{"cancelled is no timeout", context.Canceled, events.RequestOther, false, false, "RPCError"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := events.ClassifyRequestError(tc.err)
			assert.Equal(t, tc.want, c)
			assert.Equal(t, tc.notRun, c.NotRun(), "NotRun")
			assert.Equal(t, tc.retryable, c.Retryable(), "Retryable")
			assert.Equal(t, tc.reason, c.Reason(), "Reason")

			described := events.DescribeRequestError(events.RPCIndexSearch, tc.err)
			if tc.err == nil {
				assert.NoError(t, described)
				return
			}
			assert.ErrorIs(t, described, tc.err, "every sentinel survives the description")
			assert.Contains(t, described.Error(), events.RPCIndexSearch)
		})
	}
}
