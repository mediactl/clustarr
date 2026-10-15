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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/segments/worker"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/ffruntime"
)

// One NFS stall abandons one decode; the guard then decodes nothing more in
// the task. Neither the abandoned file nor the ones the guard skipped are
// recorded as Errors -- 229 such cascade Errors, and 17 seasons
// dead-lettered, on 2026-10-15 -- and the season is retried after
// AbandonRetry instead of dead-lettered.
func TestAnAbandonedDecodeRetriesTheSeasonAndRecordsNoError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stall  func(*stalling)
		before []string // recorded: analyzed before the stall
	}{
		{"in the analysis", func(s *stalling) { s.abandonFrames = map[string]bool{"e2": true} }, []string{"e1"}},
		{"while fingerprinting", func(s *stalling) { s.abandonAudio = map[string]bool{"e2": true} }, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dec := &stalling{fakeDecoder: seasonDecoder()}
			tc.stall(dec)
			h, pub := handler(t, dec.fakeDecoder, nil)
			h.Decoder, h.MaxDeliver = dec, 3
			err := h.Handle(context.Background(), season(t, 1))
			var retry *events.RetryError
			require.ErrorAs(t, err, &retry, "an abandonment is retried")
			assert.Equal(t, worker.AbandonRetry, retry.After)
			assert.ErrorIs(t, err, ffruntime.ErrAbandoned)
			s := events.Settle(err, 1, events.Subscription{MaxDeliver: 3})
			assert.Equal(t, events.SettleNak, s.Action, "not dead-lettered")
			got := pub.byFile()
			for _, f := range []string{"e1", "e2", "e3"} {
				if r, ok := got[f]; ok {
					assert.NotEqual(t, "Error", r.Result, "%s recorded as an Error for a transient abandonment", f)
				}
			}
			assert.ElementsMatch(t, tc.before, keys(got))
			assert.Zero(t, dec.frameCalls["e3"], "no FFmpeg call after the abandonment in the same task")
		})
	}
}

// On its final delivery an abandoned season is not retried into a dead
// letter: the file whose own decode was abandoned on every delivery is
// recorded as an Error -- due again in a day -- the files the guard skipped
// stay unrecorded, due at the next plan, and the task is acknowledged.
func TestTheFinalAbandonmentRecordsOnlyTheAbandonedFile(t *testing.T) {
	dec := &stalling{fakeDecoder: seasonDecoder(), abandonFrames: map[string]bool{"e2": true}}
	h, pub := handler(t, dec.fakeDecoder, nil)
	h.Decoder, h.MaxDeliver = dec, 3
	require.NoError(t, h.Handle(context.Background(), season(t, 3)), "acknowledged, never dead-lettered")
	got := pub.byFile()
	assert.Equal(t, "Found", got["e1"].Result)
	require.Equal(t, "Error", got["e2"].Result, "abandoned on its final delivery")
	require.NotNil(t, got["e2"].LastError)
	assert.Contains(t, got["e2"].LastError.Message, "abandoned")
	assert.NotContains(t, got, "e3", "only skipped: no Error of its own")
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
