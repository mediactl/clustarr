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

package fileimport

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// One plan sent twice, at two seqs, is one import: the second execute
// answers what the first placed. On talos (2026-11-27) five were answered
// "no such file" instead -- the first had moved the source -- and since that
// failure stood at a newer seq it replaced the placed answer: Ascendance of
// a Bookworm 1x27 was in the library, recorded nowhere, its entry held "the
// download holds no file", and a person's pick of another release for it
// was refused as already downloading. A plan placed by hardlink keeps its
// source and was answered rightly before; it is held to the same answer.
func TestAPlanExecutedTwiceIsAnsweredWithWhatTheFirstPlaced(t *testing.T) {
	for _, mode := range []string{schema.ImportModeMove, schema.ImportModeHardlink} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			root := filepath.Join(dir, "library")
			src := filepath.Join(dir, "complete", "Show.S01E27.1080p.mkv")
			dest := filepath.Join(root, "Show (2019)", "Season 01", "Show (2019) - S01E27.mkv")
			require.NoError(t, os.MkdirAll(filepath.Dir(src), 0o755))
			require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o755))
			require.NoError(t, os.WriteFile(src, []byte("an episode"), 0o644))
			bus := membus.New(clockwork.NewRealClock())
			require.NoError(t, bus.Ensure(ctx, events.Default()))
			t.Cleanup(func() { _ = bus.Close() })
			w := &Worker{Bus: bus}
			owner := schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: "media", Name: "show", UID: "show-uid"}}
			episode := schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Namespace: "media", Name: "show-s01e27"}}
			task := schema.ImportExecuteTask{
				Seq: 1, Owner: owner, Entry: schema.EntryRef{ID: "show-a", UID: "entry-a"},
				Plan: schema.ImportPlan{
					Mode: mode, RootFolder: root, RecycleBin: filepath.Join(dir, "recycle"),
					Moves: []schema.ImportMove{{Source: src, Dest: dest, Target: episode, MediaFileName: "show-s01e27-mf"}},
				},
			}
			run := func(task schema.ImportExecuteTask) *schema.ImportRecord {
				t.Helper()
				name, data, err := schema.Encode(task)
				require.NoError(t, err)
				require.NoError(t, w.handleExecute(ctx, &beats{}, &events.Envelope{Schema: name, Data: data}))
				rec, _, ok, err := w.reader().Get(ctx, agentrecords.ImportKey(task.Entry.UID, schema.ImportSubExecute))
				require.NoError(t, err)
				require.True(t, ok)
				return rec
			}

			first := run(task)
			require.Equal(t, records.StateAnswered, first.State, first.Failure)
			require.NotNil(t, first.Execute)
			require.Len(t, first.Execute.Placed, 1)
			require.FileExists(t, dest)

			task.Seq = 2
			second := run(task)
			assert.Equal(t, int64(2), second.Seq, "the second execute is answered at its own seq")
			assert.Equal(t, records.StateAnswered, second.State, "and not as a failure: %s", second.Failure)
			require.NotNil(t, second.Execute)
			assert.Empty(t, second.Execute.Refused, second.Failure)
			assert.Equal(t, first.Execute.Placed, second.Execute.Placed, "with the file the first placed")

			// Another plan for the entry is no repeat: it runs, and here
			// fails or is refused on its own account.
			task.Seq = 3
			task.Plan.Moves[0].MediaFileName = "show-s01e27-other"
			third := run(task)
			assert.Equal(t, int64(3), third.Seq)
			assert.False(t, third.State == records.StateAnswered && third.Execute != nil && third.Execute.Refused == "" && len(third.Execute.Placed) == 1 &&
				third.Execute.Placed[0].MediaFileName == "show-s01e27-mf", "a different plan is not answered with the earlier one's files")
		})
	}
}
