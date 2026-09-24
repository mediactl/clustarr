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

package fileimport_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
)

// The carried "spec.recycleBin.cleanupDays has no consumer: nothing empties
// the recycle bin". The sweeper removes a bin's days once they are older
// than cleanupDays; a bin shared by RootFolders keeps the longest retention
// any of them asks for; cleanupDays 0 turns a bin's cleanup off; and a bin
// that is a library's own folder is never swept.
func TestRecycleSweeperEmptiesExpiredDays(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "fi-sweep")
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	day := func(ago int) string { return now.AddDate(0, 0, -ago).Format("2006-01-02") }
	mkdirs := func(root string, names ...string) {
		for _, n := range names {
			require.NoError(t, os.MkdirAll(filepath.Join(root, n), 0o755))
		}
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }

	shared, off := dataDir(t, "recycle"), dataDir(t, "recycle")
	mkdirs(shared, day(40), day(10), "keep-me")
	mkdirs(off, day(100))
	library := dataDir(t, "media")
	mkdirs(library, day(400)) // a library folder that merely looks like a day

	folder := func(name, path, bin string, days int32) {
		require.NoError(t, c.Create(ctx, &catalogv1alpha1.RootFolder{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: catalogv1alpha1.RootFolderSpec{
				Path: path, Kind: catalogv1alpha1.RootFolderKindMovie,
				RecycleBin: catalogv1alpha1.RecycleBin{Path: bin, CleanupDays: ptr.To(days)},
			},
		}))
	}
	folder("seven", dataDir(t, "media"), shared, 7)
	folder("thirty", dataDir(t, "media"), shared, 30)
	folder("off", dataDir(t, "media"), off, 0)
	folder("library", library, dataDir(t, "recycle"), 7)
	folder("misconfigured", dataDir(t, "media"), library, 1)
	waitFor(t, 5*time.Second, func() bool {
		var list catalogv1alpha1.RootFolderList
		return c.List(ctx, &list, client.InNamespace(ns)) == nil && len(list.Items) == 5
	})

	s := &fileimport.RecycleSweeper{Client: c, Clock: func() time.Time { return now }, Namespace: ns}
	require.NoError(t, s.SweepOnce(ctx))

	assert.False(t, exists(filepath.Join(shared, day(40))), "40 days is past the longest retention sharing the bin")
	assert.True(t, exists(filepath.Join(shared, day(10))), "10 days is inside the 30 another root folder asks for")
	assert.True(t, exists(filepath.Join(shared, "keep-me")), "a folder that is not a day is never guessed at")
	assert.True(t, exists(filepath.Join(off, day(100))), "cleanupDays 0 keeps everything")
	assert.True(t, exists(filepath.Join(library, day(400))), "a bin that is a library is not swept")
}
