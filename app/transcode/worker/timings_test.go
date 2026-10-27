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

package worker

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// capturedLogs keeps every record a test's logger handles.
type capturedLogs struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturedLogs) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturedLogs) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r.Clone())
	h.mu.Unlock()
	return nil
}

func (h *capturedLogs) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturedLogs) WithGroup(string) slog.Handler      { return h }

// attrs is the attributes of the one record logged as msg.
func (h *capturedLogs) attrs(t *testing.T, msg string) map[string]slog.Value {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	var found []map[string]slog.Value
	for _, r := range h.records {
		if r.Message != msg {
			continue
		}
		m := map[string]slog.Value{}
		r.Attrs(func(a slog.Attr) bool { m[a.Key] = a.Value; return true })
		found = append(found, m)
	}
	require.Len(t, found, 1, "one %q record", msg)
	return found[0]
}

// steppingClock moves a second on at every reading.
func steppingClock() func() time.Time {
	var mu sync.Mutex
	at := time.Unix(1_800_000_000, 0)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		at = at.Add(time.Second)
		return at
	}
}

const timingsMsg = "transcode worker: stage timings"

// Every attempt logs how long each stage took, once, as it ends: a
// remux's read of its source over NFS (encode) and its copy back (stage)
// are otherwise one number between "planned" and "output placed".
func TestAnAttemptLogsHowLongEachStageTook(t *testing.T) {
	c := newSafetyCase(t, "sdr", "sdr")
	logs := &capturedLogs{}
	ctx := logging.NewContext(context.Background(), slog.New(logs))
	out := Process(ctx, c.tk, Options{
		DataDir: filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(c.local)))), ScratchDir: t.TempDir(),
		Engine: c.eng, Measurement: &transcode.Measurement{Tier: transcode.TierCPUx265}, Now: steppingClock(),
	})
	require.NoError(t, out.Err)

	got := logs.attrs(t, timingsMsg)
	var sum int64
	for _, stage := range []string{"prepareMs", "encodeMs", "verifyMs", "stageMs", "swapMs"} {
		require.Contains(t, got, stage)
		assert.Positive(t, got[stage].Int64(), stage)
		sum += got[stage].Int64()
	}
	assert.Equal(t, sum, got["totalMs"].Int64(), "the stages add up to the attempt")
	assert.Equal(t, int64(ExitOK), got["code"].Int64())
	assert.Equal(t, c.tk.SourceSizeBytes, got["sourceBytes"].Int64())
	assert.True(t, got["localScratch"].Bool())
}

// failingEncoder is a scripted engine whose encode fails.
type failingEncoder struct{ *scriptedEngine }

func (failingEncoder) Encode(context.Context, standard.Result, transcode.Tier, string, string, func(transcode.Progress)) (transcode.EncodeResult, error) {
	return transcode.EncodeResult{}, errors.New("mux: ffmpeg av_interleaved_write_frame: Invalid argument (code -22)")
}

// A failed attempt logs its timings too, ending at the stage it failed in.
func TestAFailedAttemptLogsTheStageItStoppedIn(t *testing.T) {
	c := newSafetyCase(t, "sdr", "sdr")
	logs := &capturedLogs{}
	ctx := logging.NewContext(context.Background(), slog.New(logs))
	out := Process(ctx, c.tk, Options{
		DataDir: filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(c.local)))),
		Engine:  failingEncoder{c.eng}, Measurement: &transcode.Measurement{Tier: transcode.TierCPUx265}, Now: steppingClock(),
	})
	require.Error(t, out.Err)

	got := logs.attrs(t, timingsMsg)
	assert.Contains(t, got, "prepareMs")
	assert.Contains(t, got, "encodeMs")
	assert.NotContains(t, got, "verifyMs")
	assert.NotEqual(t, int64(ExitOK), got["code"].Int64())
	assert.False(t, got["localScratch"].Bool())
}
