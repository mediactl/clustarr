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

package grabplan_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/catalog/grabplan"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// settleLines decodes the "grab candidate settled" lines a JSON handler
// captured, in order, and clears the buffer.
func settleLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &rec), line)
		if rec["msg"] == "grab candidate settled" {
			lines = append(lines, rec)
		}
	}
	buf.Reset()
	return lines
}

// TestDecideContextSaysEachSettleAtDebugWithItsReason: the re-check's
// refusal reason reached no one until 2026-11-10 -- Decide read
// verdict.reason as a guard and the plan counted the decision label alone
// -- so DecideContext says every settle at Debug through the context's
// logger: the owner, the release, the intake outcome, the decision label
// the plan counts and, for a refusal, the reason; a grab carries none.
// Decide, under a background context, logs nothing.
func TestDecideContextSaysEachSettleAtDebugWithItsReason(t *testing.T) {
	var buf bytes.Buffer
	ctx := logging.NewContext(context.Background(), slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	c := movieCandidate(t, "Heat.1995.720p.HDTV.x264-GRP")

	t.Run("a refusal names its reason beside the decision label", func(t *testing.T) {
		v := movieView(t, nil, c)
		v.Monitored = false
		p := grabplan.DecideContext(ctx, v)
		require.Empty(t, p.NewEntries)
		require.Equal(t, map[string]int{"refused": 1}, p.Decisions)
		lines := settleLines(t, &buf)
		require.Len(t, lines, 1)
		line := lines[0]
		require.Equal(t, "DEBUG", line["level"])
		require.Equal(t, "the owner is not monitored", line["reason"])
		require.Equal(t, "refused", line["decision"], "the label the plan counts")
		require.Equal(t, "refused", line["outcome"], "the intake outcome")
		require.Equal(t, "Heat.1995.720p.HDTV.x264-GRP", line["release"])
		require.Equal(t, "Movie", line["kind"])
		require.Equal(t, "heat", line["owner"])
	})

	t.Run("an item-state refusal carries the rule's reason", func(t *testing.T) {
		p := grabplan.DecideContext(ctx, movieView(t, &decision.Current{Quality: q(t, "Bluray-1080p")}, c))
		require.Empty(t, p.NewEntries)
		lines := settleLines(t, &buf)
		require.Len(t, lines, 1)
		reason, _ := lines[0]["reason"].(string)
		require.NotEmpty(t, reason)
		require.Contains(t, reason, "no item it covers wants it", "the re-check's reason, verbatim: %s", reason)
		require.Contains(t, p.Decisions, lines[0]["decision"], "the logged label is the one the plan counts")
	})

	t.Run("a grab has the label and no reason", func(t *testing.T) {
		p := grabplan.DecideContext(ctx, movieView(t, nil, c))
		require.Len(t, p.NewEntries, 1)
		lines := settleLines(t, &buf)
		require.Len(t, lines, 1)
		require.Equal(t, "grabbed", lines[0]["decision"])
		require.Equal(t, "incorporated", lines[0]["outcome"])
		_, hasReason := lines[0]["reason"]
		require.False(t, hasReason, "a grab was refused for nothing")
	})

	t.Run("Decide logs through no context", func(t *testing.T) {
		// The default logger is swapped for the capturing one, so a line
		// Decide let slip through slog's default would be seen here.
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		t.Cleanup(func() { slog.SetDefault(prev) })
		v := movieView(t, nil, c)
		v.Monitored = false
		grabplan.Decide(v)
		require.Empty(t, settleLines(t, &buf))
	})
}
