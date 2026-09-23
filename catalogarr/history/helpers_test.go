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
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	k8sevents "k8s.io/client-go/tools/events"

	"github.com/mediactl/clustarr/pkg/events"
)

// testMessage is the same minimal events.Message fake every other package in
// this tree defines locally for handler tests (see e.g.
// catalogarr/worker/grab/handler_envtest_test.go); there is no shared one to
// import.
type testMessage struct {
	env     *events.Envelope
	subject string
}

func (m testMessage) Envelope() *events.Envelope             { return m.env }
func (m testMessage) Subject() string                        { return m.subject }
func (testMessage) Attempt() uint64                          { return 1 }
func (testMessage) Ack(context.Context) error                { return nil }
func (testMessage) Nak(context.Context, time.Duration) error { return nil }
func (testMessage) Term(context.Context, string) error       { return nil }
func (testMessage) InProgress(context.Context) error         { return nil }

// fakeEvent is one call recorded by fakeRecorder.
type fakeEvent struct {
	regarding                       runtime.Object
	eventtype, reason, action, note string
}

var _ k8sevents.EventRecorder = (*fakeRecorder)(nil)

// fakeRecorder is a k8sevents.EventRecorder that records calls instead of
// sending them anywhere, so a test can assert exactly what Sink and
// DLQProjector decided to report without needing a live events.k8s.io sink.
type fakeRecorder struct {
	mu     sync.Mutex
	events []fakeEvent
}

func (r *fakeRecorder) Eventf(regarding, _ runtime.Object, eventtype, reason, action, note string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, fakeEvent{
		regarding: regarding, eventtype: eventtype, reason: reason, action: action,
		note: fmt.Sprintf(note, args...),
	})
}

func (r *fakeRecorder) AnnotatedEventf(regarding, related runtime.Object, _ map[string]string, eventtype, reason, action, note string, args ...any) {
	r.Eventf(regarding, related, eventtype, reason, action, note, args...)
}
