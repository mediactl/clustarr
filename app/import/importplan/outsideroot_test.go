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

package importplan_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// TestAReplacedFileOutsideTheRootFolderIsNeverRecycled is the smoke test's
// T2 data-safety finding: an upgrade replaces the item's MediaFiles (P61),
// and the import agent moved each into the recycle bin wherever its
// spec.path pointed -- a MediaFile recording a file outside the item's
// RootFolder took that file out of a folder the library does not own. A
// path outside the root folder is never touched (the librarydelete rule):
// the planner holds the import for a person (needsPerson), and the agent
// refuses a plan that names one, before it places or recycles anything.
func TestAReplacedFileOutsideTheRootFolderIsNeverRecycled(t *testing.T) {
	outside := func(wd *world) (string, *catalogv1alpha1.MediaFile) {
		path := filepath.Join(wd.dir, "elsewhere", "Ronin (1998)", "Ronin (1998) HDTV-720p.mkv")
		return path, wd.mediaFile("ronin-elsewhere", commonv1.MediaKindMovie, "ronin", path, "HDTV-720p")
	}
	binEmpty := func(t *testing.T, wd *world) {
		t.Helper()
		entries, err := os.ReadDir(wd.bin())
		if !os.IsNotExist(err) {
			require.NoError(t, err)
		}
		assert.Empty(t, entries, "nothing went to the recycle bin")
	}

	t.Run("the planner holds the import for a person", func(t *testing.T) {
		wd := newWorld(t)
		oldPath, mf := outside(wd)
		wd.add(mf)
		root := wd.payload(map[string]int64{"Ronin.1998.1080p.BluRay.x264-GRP.mkv": big})
		g := wd.grab(commonv1.MediaKindMovie, "ronin", "Ronin.1998.1080p.BluRay.x264-GRP", root)

		dec := g.inspected()
		concluded(t, dec, commonv1.ImportClassNeedsPerson, lifecycle.ImportHeld, oldPath, "not inside root folder")
		assert.FileExists(t, oldPath, "the file outside the root folder is where it was")
		binEmpty(t, wd)
	})

	t.Run("the agent refuses a plan that names one, whatever the manager decided", func(t *testing.T) {
		wd := newWorld(t)
		root := wd.payload(map[string]int64{"Ronin.1998.1080p.BluRay.x264-GRP.mkv": big})
		g := wd.grab(commonv1.MediaKindMovie, "ronin", "Ronin.1998.1080p.BluRay.x264-GRP", root)
		g.inspected()
		task := g.plan()
		require.Len(t, task.Plan.Moves, 1)
		dest := task.Plan.Moves[0].Dest

		// The plan names a MediaFile outside the root folder (a manager
		// from before the planner refused one): its basis holds, so only
		// the containment refuses it.
		oldPath, mf := outside(wd)
		wd.add(mf)
		require.NoError(t, wd.c.Get(context.Background(), client.ObjectKeyFromObject(mf), mf))
		task.Plan.Replaces = []schema.MediaFileBasis{{
			Name: mf.Name, UID: string(mf.UID), ResourceVersion: mf.ResourceVersion, Path: oldPath,
		}}

		dec := g.executed()
		rec := g.record(schema.ImportSubExecute)
		require.NotNil(t, rec)
		require.NotNil(t, rec.Execute)
		assert.Equal(t, schema.ImportRefusedOutsideRoot, rec.Execute.Refused, rec.Failure)
		assert.Contains(t, rec.Failure, oldPath)
		assert.Empty(t, rec.Execute.Recycled)
		assert.Empty(t, rec.Execute.Replaced, "no MediaFile is deleted for it")
		assert.Empty(t, rec.Execute.Placed, "refused before anything is placed")
		assert.FileExists(t, oldPath, "the file outside the root folder is where it was")
		assert.NoFileExists(t, dest)
		binEmpty(t, wd)

		concluded(t, dec, commonv1.ImportClassNeedsPerson, lifecycle.ImportHeld, oldPath)
		assert.NotEqual(t, lifecycle.ImportReleaseFault, dec.Verdict, "never the release's fault")
	})
}
