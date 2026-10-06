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

package importarr

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeWrites is a write probe the test releases by hand, so a probe can be
// held as slow as a saturated NFS mount makes it (10 s on kind-cluster-plex,
// 2026-10-05, against the kubelet's 1 s probe timeout).
type fakeWrites struct {
	calls   atomic.Int32
	mu      sync.Mutex
	err     error
	release chan struct{}
}

func (f *fakeWrites) probe(string) error {
	f.calls.Add(1)
	f.mu.Lock()
	release, err := f.release, f.err
	f.mu.Unlock()
	if release != nil {
		<-release
	}
	return err
}

// hold makes the next probes block until the returned func is called.
func (f *fakeWrites) hold() func() {
	ch := make(chan struct{})
	f.mu.Lock()
	f.release = ch
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.release = nil
		f.mu.Unlock()
		close(ch)
	}
}

func (f *fakeWrites) fail(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestDataCheck(w *fakeWrites, c *fakeClock) *dataCheck {
	return &dataCheck{
		path:     "/data",
		write:    w.probe,
		now:      c.now,
		interval: 30 * time.Second,
		stall:    time.Minute,
		wait:     20 * time.Millisecond,
	}
}

// A probe slower than the kubelet's timeout must not fail readiness: the
// check answers from the last finished probe while the slow one runs.
func TestDataCheckAnswersFromTheLastProbeWhileASlowOneRuns(t *testing.T) {
	w, c := &fakeWrites{}, &fakeClock{t: time.Unix(0, 0)}
	d := newTestDataCheck(w, c)
	require.NoError(t, d.Check(nil), "a fast first probe answers at once")

	release := w.hold()
	defer release()
	c.advance(31 * time.Second)
	start := time.Now()
	require.NoError(t, d.Check(nil), "the slow probe's predecessor succeeded")
	require.Less(t, time.Since(start), time.Second, "the check waited on the slow probe")
	require.Equal(t, int32(2), w.calls.Load(), "the stale result started a new probe")
}

// Only one probe runs at a time: a hung NFS write must not pile up a
// goroutine per kubelet probe.
func TestDataCheckRunsOneProbeAtATime(t *testing.T) {
	w, c := &fakeWrites{}, &fakeClock{t: time.Unix(0, 0)}
	d := newTestDataCheck(w, c)
	release := w.hold()
	defer release()
	for range 5 {
		_ = d.Check(nil)
		c.advance(31 * time.Second)
	}
	require.Equal(t, int32(1), w.calls.Load())
}

// A probe that has not finished after the stall limit fails readiness: the
// mount is hung, not slow.
func TestDataCheckFailsAProbeStalledPastTheLimit(t *testing.T) {
	w, c := &fakeWrites{}, &fakeClock{t: time.Unix(0, 0)}
	d := newTestDataCheck(w, c)
	require.NoError(t, d.Check(nil))

	release := w.hold()
	c.advance(31 * time.Second)
	require.NoError(t, d.Check(nil), "the probe has just started")
	c.advance(59 * time.Second)
	require.NoError(t, d.Check(nil), "the probe is slow, not stalled")
	c.advance(2 * time.Second)
	err := d.Check(nil)
	require.ErrorContains(t, err, "/data")
	require.ErrorContains(t, err, "not finished")

	release()
	require.Eventually(t, func() bool { return d.Check(nil) == nil }, time.Second, 5*time.Millisecond,
		"the stalled probe finished and succeeded")
}

// Before any probe has finished the pod is not ready, and once the first
// slow probe finishes it is.
func TestDataCheckIsNotReadyUntilAProbeFinishes(t *testing.T) {
	w, c := &fakeWrites{}, &fakeClock{t: time.Unix(0, 0)}
	d := newTestDataCheck(w, c)
	release := w.hold()
	require.ErrorContains(t, d.Check(nil), "/data")
	release()
	require.Eventually(t, func() bool { return d.Check(nil) == nil }, time.Second, 5*time.Millisecond)
}

// A failed write is reported, and the next check probes again rather than
// waiting out the interval, so a recovered mount is ready at once.
func TestDataCheckReportsAFailureAndRetriesAtOnce(t *testing.T) {
	w, c := &fakeWrites{}, &fakeClock{t: time.Unix(0, 0)}
	d := newTestDataCheck(w, c)
	w.fail(errors.New("read-only file system"))
	require.ErrorContains(t, d.Check(nil), "read-only file system")

	w.fail(nil)
	require.NoError(t, d.Check(nil))
	require.Equal(t, int32(2), w.calls.Load())
}

// A fresh success is reused: the kubelet's probe every 10 s does not touch
// the mount each time.
func TestDataCheckReusesAFreshResult(t *testing.T) {
	w, c := &fakeWrites{}, &fakeClock{t: time.Unix(0, 0)}
	d := newTestDataCheck(w, c)
	for range 3 {
		require.NoError(t, d.Check(nil))
		c.advance(10 * time.Second)
	}
	require.Equal(t, int32(1), w.calls.Load())
}
