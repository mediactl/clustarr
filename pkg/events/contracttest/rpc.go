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

package contracttest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// The request/reply cases below hold both buses to the rules in
// pkg/events/serve.go. Each is falsified against the natsbus that ran its
// handler inside the nats.go callback: RequestReplyHandlersRunConcurrently,
// RequestReplyExpiredCallerIsNotRun and RequestReplyHandlerDeadlineFollowsCaller
// time out or fail there, and RequestReplyPanicKeepsServing crashed the
// process on both buses.

// blockingResponder serves subject with a handler that counts the requests
// running in it and blocks each until release is closed or its context
// ends. Requests whose body is not a blocker are recorded in seen.
type blockingResponder struct {
	running atomic.Int32
	release chan struct{}
	once    sync.Once

	mu   sync.Mutex
	seen []string
}

func serveBlocking(t *testing.T, bus events.Bus, subject string) *blockingResponder {
	t.Helper()
	r := &blockingResponder{release: make(chan struct{})}
	t.Cleanup(r.free)
	err := bus.Serve(subject, events.QueueGroupIndex,
		func(ctx context.Context, data []byte) ([]byte, error) {
			body := string(data)
			if body != `"block"` {
				r.mu.Lock()
				r.seen = append(r.seen, body)
				r.mu.Unlock()
				return data, nil
			}
			r.running.Add(1)
			defer r.running.Add(-1)
			select {
			case <-r.release:
				return data, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	return r
}

// free lets every blocked handler return.
func (r *blockingResponder) free() { r.once.Do(func() { close(r.release) }) }

func (r *blockingResponder) saw(body string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.seen {
		if s == body {
			return true
		}
	}
	return false
}

// waitRunning waits until n blocking handlers are running at once.
func (r *blockingResponder) waitRunning(t *testing.T, n int32) {
	t.Helper()
	deadline := time.Now().Add(Timeout / 2)
	for r.running.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d handlers running at once: Serve does not run them concurrently",
				r.running.Load(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// testServeConcurrency sends N requests at once to one Serve whose handler
// returns only once all N are inside it. A bus that answers one request at a
// time never gets the second in, and every request times out.
func testServeConcurrency(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	const n = 8
	var arrived atomic.Int32
	all := make(chan struct{})
	err := bus.Serve(events.RPCIndexSearch, events.QueueGroupIndex,
		func(ctx context.Context, data []byte) ([]byte, error) {
			if arrived.Add(1) == n {
				close(all)
			}
			select {
			case <-all:
				return data, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	errs := make(chan error, n)
	for range n {
		go func() {
			var out string
			errs <- bus.Request(reqCtx, events.RPCIndexSearch, "q", &out)
		}()
	}
	for range n {
		if err := <-errs; err != nil {
			t.Errorf("Request: %v (%d of %d handlers ever ran at once: Serve is serial)",
				err, arrived.Load(), n)
		}
	}
}

// testServePanic: a handler that panics answers its requester with
// ErrResponderFailed, and the responder keeps serving.
func testServePanic(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	err := bus.Serve(events.RPCIndexSearch, events.QueueGroupIndex,
		func(_ context.Context, data []byte) ([]byte, error) {
			if string(data) == `"boom"` {
				// A real runtime panic, as a handler parsing a provider
				// response it did not expect would raise: index out of range.
				fields := strings.Split(string(data), ",")
				return []byte(fields[2]), nil
			}
			return data, nil
		})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var out string
	err = bus.Request(reqCtx, events.RPCIndexSearch, "boom", &out)
	if !errors.Is(err, events.ErrResponderFailed) {
		t.Fatalf("Request to a panicking handler error = %v, want ErrResponderFailed", err)
	}
	if !strings.Contains(err.Error(), "panicked") {
		t.Errorf("panic error %q does not say the handler panicked", err)
	}
	if err := bus.Request(reqCtx, events.RPCIndexSearch, "fine", &out); err != nil {
		t.Fatalf("Request after a panic: %v", err)
	}
	if out != "fine" {
		t.Errorf("reply after a panic = %q, want %q", out, "fine")
	}
}

// errLookupContract is a handler's own sentinel, which must not survive the
// trip back to the requester on any bus: natsbus can carry only its text.
var errLookupContract = errors.New("contract: title not found")

// testServeErrorSemantics: a handler error reaches the requester as a
// *ResponderError carrying its text, matching ErrResponderFailed and never
// the handler's own sentinel -- identically on every bus.
func testServeErrorSemantics(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	err := bus.Serve(events.RPCIndexSearch, events.QueueGroupIndex,
		func(context.Context, []byte) ([]byte, error) {
			return nil, errors.Join(errors.New("lookup tt0001"), errLookupContract)
		})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}

	err = bus.Request(ctx, events.RPCIndexSearch, "q", nil)
	if err == nil {
		t.Fatal("Request to a failing handler succeeded")
	}
	if !errors.Is(err, events.ErrResponderFailed) {
		t.Errorf("error %v does not match ErrResponderFailed", err)
	}
	if errors.Is(err, events.ErrResponderBusy) {
		t.Errorf("a handler failure matches ErrResponderBusy: %v", err)
	}
	if errors.Is(err, errLookupContract) {
		t.Errorf("the handler's sentinel survived the reply on this bus but cannot over NATS: %v", err)
	}
	var re *events.ResponderError
	if !errors.As(err, &re) {
		t.Fatalf("error %T %v is not a *ResponderError", err, err)
	}
	if re.Code != events.ServiceErrorFailed {
		t.Errorf("ResponderError.Code = %d, want %d", re.Code, events.ServiceErrorFailed)
	}
	// One line: a NATS header cannot carry a line break.
	if want := "lookup tt0001 contract: title not found"; re.Message != want {
		t.Errorf("ResponderError.Message = %q, want %q", re.Message, want)
	}
	if !strings.Contains(err.Error(), events.RPCIndexSearch) {
		t.Errorf("error %q does not name the subject", err)
	}
}

// testServeExpiredCaller: a request still waiting for a handler slot when
// its caller's deadline passes is never run, so a responder does not spend
// a slot on an answer nobody is listening for.
func testServeExpiredCaller(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	r := serveBlocking(t, bus, events.RPCIndexSearch)

	for range events.DefaultServeConcurrency {
		go func() { _ = bus.Request(ctx, events.RPCIndexSearch, "block", nil) }()
	}
	r.waitRunning(t, events.DefaultServeConcurrency)

	lateCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	err := bus.Request(lateCtx, events.RPCIndexSearch, "late", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Request queued behind full slots error = %v, want DeadlineExceeded", err)
	}
	// The responder measures the caller's time from receipt, a transit
	// later than the caller did; let that pass before freeing the slots.
	time.Sleep(250 * time.Millisecond)
	r.free()

	var out string
	if err := bus.Request(ctx, events.RPCIndexSearch, "after", &out); err != nil {
		t.Fatalf("Request after the slots freed: %v", err)
	}
	if r.saw(`"late"`) {
		t.Error("the handler ran a request whose caller had already given up")
	}
}

// testServeBusy: a request that finds every slot and queue place taken is
// refused at once with ErrResponderBusy; the rest are answered.
func testServeBusy(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	r := serveBlocking(t, bus, events.RPCIndexSearch)

	total := events.DefaultServeConcurrency + events.DefaultServeQueue + 1
	errs := make(chan error, total)
	for range total {
		go func() {
			errs <- bus.Request(ctx, events.RPCIndexSearch, "block", nil)
		}()
	}
	var busy, failed int
	select {
	case err := <-errs:
		if !errors.Is(err, events.ErrResponderBusy) {
			t.Fatalf("first request to finish, with every slot held, error = %v, want ErrResponderBusy", err)
		}
		busy++
	case <-time.After(Timeout / 2):
		t.Fatal("no request was refused with every slot and queue place taken")
	}
	r.free()
	for range total - 1 {
		switch err := <-errs; {
		case errors.Is(err, events.ErrResponderBusy):
			busy++
		case err != nil:
			failed++
			t.Errorf("Request: %v", err)
		}
	}
	if busy != 1 || failed != 0 {
		t.Errorf("%d of %d requests refused busy and %d failed, want exactly 1 and 0",
			busy, total, failed)
	}
}

// testServeDeadlineFollowsCaller: the handler's deadline is the caller's,
// not the bus's whole request timeout.
func testServeDeadlineFollowsCaller(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	got := make(chan time.Duration, 1)
	err := bus.Serve(events.RPCIndexSearch, events.QueueGroupIndex,
		func(ctx context.Context, data []byte) ([]byte, error) {
			left := time.Duration(-1)
			if dl, ok := ctx.Deadline(); ok {
				left = time.Until(dl)
			}
			got <- left
			return data, nil
		})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	const budget = 3 * time.Second
	reqCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if err := bus.Request(reqCtx, events.RPCIndexSearch, "q", nil); err != nil {
		t.Fatalf("Request: %v", err)
	}
	switch left := <-got; {
	case left < 0:
		t.Error("the handler's context has no deadline")
	case left > budget:
		t.Errorf("the handler had %s left, more than its caller's %s", left, budget)
	}
}
