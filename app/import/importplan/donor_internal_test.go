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

package importplan

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// TestReplacingADonorKeepsTheNewOne: an old donor <item>.mkv already reduced
// to <item>.mka, replaced by a new donor that is itself <item>.mka -- the
// old donor's removal must not take the file just placed (final review).
// The removal is the planner's to name since A3.8 (materialiseDonor).
func TestReplacingADonorKeepsTheNewOne(t *testing.T) {
	const dir = "/data/media/tv/.clustarr/donors/9b2c"
	placed := filepath.Join(dir, "monster-s01e02.mka")
	removed := func(recorded string) []string {
		p := &planner{in: Input{Donor: &DonorItem{RecordedDonor: recorded}, Inspect: inspected("/data/media/tv")}}
		p.materialiseDonor(&schema.ImportExecution{DonorPath: placed})
		require.Equal(t, lifecycle.ImportImported, p.dec.Verdict)
		var out []string
		for _, m := range p.dec.Materialise {
			out = append(out, m.Remove...)
		}
		return out
	}

	got := removed(filepath.Join(dir, "monster-s01e02.mkv"))
	require.NotContains(t, got, placed)
	require.Equal(t, []string{filepath.Join(dir, "monster-s01e02.mkv")}, got)

	other := filepath.Join(dir, "old.mkv")
	require.ElementsMatch(t, []string{other, filepath.Join(dir, "old.mka")}, removed(other))
}

// inspected is an inspect record of an import under root.
func inspected(root string) *schema.ImportRecord {
	return &schema.ImportRecord{Inspect: &schema.ImportInspection{RootFolder: root}}
}

// TestASupersededDonorOutsideTheDonorFolderIsKept is smoke T2's sweep of the
// import's other removals: the donor a newer one supersedes is removed by
// the manager (fsops.SafeRemove under the data root, not the root folder),
// so a recorded donor path that is not under the root folder's donor folder
// -- a library file, another root folder, a path the planner cannot place --
// is never removed; the import is still imported, and says what it kept.
func TestASupersededDonorOutsideTheDonorFolderIsKept(t *testing.T) {
	const root = "/data/media/tv"
	placed := filepath.Join(root, ".clustarr/donors/9b2c/monster-s01e02.mka")
	for _, recorded := range []string{
		"/data/media/tv/Monster/Season 01/Monster - S01E02.mkv",
		"/data/media/movies/.clustarr/donors/9b2c/monster-s01e02.mkv",
		"/data/downloads/monster-s01e02.mkv",
		"/data/media/tv/.clustarr/donors/../../Monster/Season 01/Monster - S01E02.mkv",
	} {
		p := &planner{in: Input{Donor: &DonorItem{RecordedDonor: recorded}, Inspect: inspected(root)}}
		p.materialiseDonor(&schema.ImportExecution{DonorPath: placed})
		require.Equal(t, lifecycle.ImportImported, p.dec.Verdict, recorded)
		require.Empty(t, p.dec.Materialise, "%s is never removed", recorded)
		require.Contains(t, p.sum.Message, recorded)
	}

	// Without the inspection naming the root folder, nothing is removed.
	p := &planner{in: Input{Donor: &DonorItem{RecordedDonor: filepath.Join(root, ".clustarr/donors/9b2c/old.mkv")}}}
	p.materialiseDonor(&schema.ImportExecution{DonorPath: placed})
	require.Equal(t, lifecycle.ImportImported, p.dec.Verdict)
	require.Empty(t, p.dec.Materialise)
}
