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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/catalog/grabplan"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// A grab leaves one Info line saying where its candidate came from and
// what the planner read of the file of each episode it is taken for: a
// file of another release, the file this very release was imported as,
// and an episode that read as having none.
func TestAGrabLogsWhatThePlannerReadOfEachItem(t *testing.T) {
	cands := seasonCandidates(t, 1, 3, pack1080)
	covered := season(1, 3)
	covered[0].Current = &decision.Current{Quality: q(t, "HDTV-720p")}
	covered[1].Current = &decision.Current{Quality: q(t, "HDTV-720p"), SourceTitle: pack1080}
	var buf bytes.Buffer
	ctx := logging.NewContext(context.Background(), slog.New(slog.NewJSONHandler(&buf, nil)))
	p := grabplan.DecideContext(ctx, seriesView(t, covered, cands...))
	require.Len(t, p.NewEntries, 1)

	var line struct {
		Msg, Owner, Entry, Release, Quality, Source, By string
		Manual, Override                                bool
		Covered                                         int
		Over                                            []string
	}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &line), buf.String())
	require.Equal(t, "grab: taking a release", line.Msg)
	require.Equal(t, p.NewEntries[0].ID, line.Entry)
	require.Equal(t, pack1080, line.Release)
	require.Equal(t, "answer", line.Source)
	require.Equal(t, "search", line.By)
	require.False(t, line.Manual)
	require.Equal(t, 3, line.Covered)
	require.Equal(t, []string{
		"breaking-bad-s01e01=HDTV-720p",
		"breaking-bad-s01e03=none",
	}, line.Over, "the episode whose file is this release is not one the pack is taken for")
}
