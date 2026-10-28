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
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// lockedBuffer is a bytes.Buffer a handler's goroutine writes and the test
// reads.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestAServedHandlerLogsOnTheBussLogger: a responder's handler logs through
// logging.FromContext, and its context carried no logger, so every line an
// RPC handler wrote was discarded. On talos (2026-10-28) the index agent's
// download verb answered 15,403 grab-limit refusals and recorded a 429
// pause in 43 minutes, and logged none of it.
func TestAServedHandlerLogsOnTheBussLogger(t *testing.T) {
	var out lockedBuffer
	bus, err := natsbus.New(connect(t), natsbus.WithLogger(slog.New(slog.NewTextHandler(&out, nil))))
	if err != nil {
		t.Fatalf("natsbus.New: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	stop, err := bus.Serve(events.RPCIndexDownload, events.QueueGroupIndex,
		func(ctx context.Context, _ []byte) ([]byte, error) {
			logging.FromContext(ctx).Info("the handler ran", "guid", "g")
			return []byte(`{}`), nil
		})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(stop)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var reply map[string]any
	if err := bus.Request(ctx, events.RPCIndexDownload, map[string]string{"guid": "g"}, &reply); err != nil {
		t.Fatalf("Request: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "the handler ran") {
		t.Fatalf("the handler's line never reached the bus's logger; it logged %q", got)
	}
}
