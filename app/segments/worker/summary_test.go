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
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/segments/worker"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Every task ends in one INFO line saying what became of it, so a healthy
// pod's log is not FFmpeg's warnings alone.
func TestEveryTaskLogsOneSummary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dec     func() worker.Decoder
		attempt uint64
		want    map[string]any
	}{
		{
			"acked", func() worker.Decoder { return seasonDecoder() }, 1,
			map[string]any{"outcome": "acked", "abandoned": false, "files": int64(3), "found": int64(3), "deferred": int64(0)},
		},
		{"retried after an abandonment", func() worker.Decoder {
			return &stalling{fakeDecoder: seasonDecoder(), abandonFrames: map[string]bool{"e2": true}}
		}, 1, map[string]any{"outcome": "retrying", "abandoned": true, "found": int64(1), "deferred": int64(2)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := handler(t, seasonDecoder(), nil)
			h.Decoder, h.MaxDeliver = tc.dec(), 3
			rec := &recorder{}
			ctx := logging.NewContext(context.Background(), slog.New(rec))
			_ = h.Handle(ctx, season(t, tc.attempt))
			lines := rec.named("markers: task finished")
			require.Len(t, lines, 1, "one summary line per task")
			l := lines[0]
			assert.Equal(t, slog.LevelInfo, l.level)
			assert.Equal(t, "media/show-s01", l.attrs["key"])
			assert.Contains(t, l.attrs, "duration")
			for k, v := range tc.want {
				assert.Equal(t, v, l.attrs[k], k)
			}
		})
	}
}

// recorder is a slog.Handler keeping every record.
type recorder struct {
	mu    sync.Mutex
	lines []line
}

type line struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

func (r *recorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *recorder) WithAttrs([]slog.Attr) slog.Handler       { return r }
func (r *recorder) WithGroup(string) slog.Handler            { return r }
func (r *recorder) Handle(_ context.Context, rec slog.Record) error {
	l := line{level: rec.Level, msg: rec.Message, attrs: map[string]any{}}
	rec.Attrs(func(a slog.Attr) bool {
		l.attrs[a.Key] = a.Value.Resolve().Any()
		return true
	})
	r.mu.Lock()
	r.lines = append(r.lines, l)
	r.mu.Unlock()
	return nil
}

func (r *recorder) named(msg string) []line {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []line
	for _, l := range r.lines {
		if l.msg == msg {
			out = append(out, l)
		}
	}
	return out
}
