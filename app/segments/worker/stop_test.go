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

package worker_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/ffruntime"
)

// stalling serves fakeDecoder's files, except that Frames of the files in
// hang blocks until its context ends, and Audio or Frames of the files in
// abandon fails as ffruntime.Do fails a call it had to abandon: an NFS
// read that outlived its context plus Grace.
type stalling struct {
	*fakeDecoder
	hang, abandonAudio, abandonFrames map[string]bool
	entered                           chan string
}

func abandoned() error {
	return fmt.Errorf("decode: %w: %w", ffruntime.ErrAbandoned, context.DeadlineExceeded)
}

func (s *stalling) Audio(ctx context.Context, path string, stream int, fromS, durS float64) ([]int16, error) {
	if s.abandonAudio[path] {
		return nil, abandoned()
	}
	return s.fakeDecoder.Audio(ctx, path, stream, fromS, durS)
}

func (s *stalling) Frames(ctx context.Context, path string, fromS float64) ([][]byte, error) {
	if s.hang[path] {
		if s.entered != nil {
			s.entered <- path
		}
		<-ctx.Done()
		return nil, fmt.Errorf("decode: %w", ctx.Err())
	}
	if s.abandonFrames[path] {
		s.fakeDecoder.mu.Lock()
		if s.fakeDecoder.frameCalls == nil {
			s.fakeDecoder.frameCalls = map[string]int{}
		}
		s.fakeDecoder.frameCalls[path]++
		s.fakeDecoder.mu.Unlock()
		return nil, abandoned()
	}
	return s.fakeDecoder.Frames(ctx, path, fromS)
}

// delivery is a task at a given delivery attempt.
type delivery struct {
	message
	attempt uint64
}

func (d delivery) Attempt() uint64 { return d.attempt }

func season(t *testing.T, attempt uint64) events.Message {
	t.Helper()
	m := taskOf(t, "episode", "e1", "e2", "e3")
	return delivery{message: message{m.Envelope()}, attempt: attempt}
}

func seasonDecoder() *fakeDecoder {
	return &fakeDecoder{
		introAt: map[string]float64{"e1": 10, "e2": 10, "e3": 10},
		black:   map[string]int{"e1": 30, "e2": 30, "e3": 30},
	}
}

// A rollout's SIGTERM cuts a season off: the decode in flight ends at once,
// nothing it was doing is recorded -- a cancelled decode is no Error -- and
// the task is handed back (events.Requeue), which spends none of its
// deliveries. What finished before the stop stays recorded.
func TestAStopRequeuesTheTaskAndRecordsNothingItCutOff(t *testing.T) {
	dec := &stalling{fakeDecoder: seasonDecoder(), hang: map[string]bool{"e2": true}, entered: make(chan string, 1)}
	h, pub := handler(t, dec.fakeDecoder, nil)
	h.Decoder = dec
	ctx, stop := context.WithCancelCause(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Handle(ctx, season(t, 1)) }()
	select {
	case <-dec.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("e2's frames were never decoded")
	}
	stop(events.ErrStopping)
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the task kept running after the stop")
	}
	var rq *events.RequeueError
	require.ErrorAs(t, err, &rq, "a stopped task is requeued, not retried or dead-lettered")
	got := pub.byFile()
	assert.Equal(t, "Found", got["e1"].Result, "analyzed before the stop: kept")
	assert.NotContains(t, got, "e2", "cut off by the stop: not an Error")
	assert.NotContains(t, got, "e3", "never reached")
}
