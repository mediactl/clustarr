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
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// TestRecycleFilesRefusesAPathOutsideTheRootFolder (A6.2, data safety): a
// recycle.files task takes files out of a library, so every path it names
// must lie strictly under the task's RootFolder -- a path outside it, a
// sibling folder sharing its prefix, one that climbs out through "..", or the
// root folder itself refuses the whole task before anything moves, and the
// refusal is dead-lettered (no retry makes it safe). A path inside it is
// recycled with its subtitle sidecar, never unlinked, and the folders it
// leaves empty are pruned up to the root folder.
func TestRecycleFilesRefusesAPathOutsideTheRootFolder(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "fi-recycle-files")
	library, bin, elsewhere := dataDir(t, "media"), dataDir(t, "recycle"), dataDir(t, "media")
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: ns},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: library, Kind: catalogv1alpha1.RootFolderKindMovie,
			RecycleBin: catalogv1alpha1.RecycleBin{Path: bin},
		},
	}
	require.NoError(t, c.Create(ctx, rf))
	waitFor(t, 5*time.Second, func() bool {
		return c.Get(ctx, client.ObjectKeyFromObject(rf), &catalogv1alpha1.RootFolder{}) == nil
	})

	write := func(p string) string {
		t.Helper()
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(filepath.Base(p)), 0o644))
		return p
	}
	inside := write(filepath.Join(library, "Heat (1995)", "Heat (1995).mkv"))
	sidecar := write(filepath.Join(library, "Heat (1995)", "Heat (1995).en.srt"))
	outside := write(filepath.Join(elsewhere, "Ronin (1998)", "Ronin (1998).mkv"))
	sibling := write(filepath.Join(library+"-old", "Heat (1995)", "Heat (1995).mkv"))
	t.Cleanup(func() { _ = os.RemoveAll(library + "-old") })

	h := fileimport.NewRecycleFiles(c)
	handle := func(paths ...string) error {
		name, data, err := schema.Encode(schema.RecycleFilesTask{
			RootFolder: schema.Ref{Namespace: ns, Name: rf.Name, UID: string(rf.UID)}, Paths: paths,
		})
		require.NoError(t, err)
		return h.Handle(ctx, &fakeMessage{env: &events.Envelope{Schema: name, Data: data}})
	}
	binEntries := func() []os.DirEntry {
		es, err := os.ReadDir(bin)
		require.NoError(t, err)
		return es
	}

	for _, tc := range []struct {
		name  string
		paths []string
	}{
		{"a path in another folder, after one inside", []string{inside, outside}},
		{"a sibling folder sharing the root folder's prefix", []string{sibling}},
		{"a path that climbs out through dot-dot", []string{filepath.Join(library, "Heat (1995)", "..", "..", filepath.Base(elsewhere), "Ronin (1998)", "Ronin (1998).mkv")}},
		{"the root folder itself", []string{library}},
		{"a relative path", []string{"Heat (1995)/Heat (1995).mkv"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := handle(tc.paths...)
			var discard *events.DiscardError
			require.ErrorAs(t, err, &discard, "a refusal is dead-lettered, not retried")
			require.ErrorIs(t, err, fileimport.ErrOutsideRootFolder)
			for _, p := range []string{inside, sidecar, outside, sibling} {
				assert.FileExists(t, p, "nothing moves when any named path is refused")
			}
			assert.Empty(t, binEntries(), "nothing reached the recycle bin")
		})
	}

	// The control: a path inside the root folder goes to the bin with its
	// sidecar, and its emptied folder is pruned; the root folder stays.
	require.NoError(t, handle(inside))
	assert.NoFileExists(t, inside)
	assert.NoFileExists(t, sidecar)
	assert.NoDirExists(t, filepath.Join(library, "Heat (1995)"), "the emptied folder is pruned")
	assert.DirExists(t, library, "never the root folder")
	assert.FileExists(t, outside)
	require.Len(t, binEntries(), 1, "one day folder")
	day := filepath.Join(bin, binEntries()[0].Name())
	recycled, err := os.ReadDir(day)
	require.NoError(t, err)
	var names []string
	for _, e := range recycled {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{"Heat (1995).mkv", "Heat (1995).en.srt"}, names, "recycled, never unlinked")
}
