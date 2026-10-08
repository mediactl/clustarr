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
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/import/importplan"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// movieRun is an importRun of one automatic grab of the fixture's movie,
// whose payload is contentRoot.
func (f *fixture) movieRun(t *testing.T, entryUID, contentRoot, title string, now time.Time) *importRun {
	t.Helper()
	var movie catalogv1alpha1.Movie
	require.NoError(t, f.api.Get(context.Background(), client.ObjectKey{Namespace: f.ns, Name: f.movieName}, &movie))
	prof, errs := quality.FromCRD(f.profile, catalogue.LoadedCatalogue())
	require.Empty(t, errs)
	owner := schema.ItemRef{Kind: "Movie", Ref: schema.Ref{Namespace: f.ns, Name: movie.Name, UID: string(movie.UID)}}
	f.worker.Clock = func() time.Time { return now }
	return &importRun{
		t: t, w: f.worker, bus: f.bus, existing: f.api,
		in: importplan.Input{
			Owner: owner, OwnerKind: commonv1.MediaKindMovie,
			Entry: catalogv1alpha1.DownloadEntry{
				ID: "dl-" + entryUID, UID: entryUID,
				Release:   catalogv1alpha1.DownloadRelease{Title: title, Protocol: commonv1.ProtocolTorrent},
				GrabbedBy: commonv1.GrabSourceSearch, GrabbedAt: metav1.NewTime(now.Add(-time.Hour)),
				Phase: commonv1.DownloadPhaseCompleted,
			},
			Transfer:   &schema.TransferRecord{ContentRoot: contentRoot},
			Targets:    []schema.ItemRef{owner},
			ProfileRef: f.profile.Name, Profile: &prof, OriginalLanguage: "en",
			Now: now,
		},
	}
}

// existingMovieFile creates a MediaFile for the fixture's movie at path, an
// HDTV-720p file the grab's 1080p Bluray upgrades.
func (f *fixture) existingMovieFile(t *testing.T, name, path string) *catalogv1alpha1.MediaFile {
	t.Helper()
	ctx := context.Background()
	mustWriteSparseFile(t, path, sampleFloor)
	def, ok := quality.Lookup("video", "HDTV-720p")
	require.True(t, ok)
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Namespace: f.ns, Name: name},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName},
			Path:     path, Quality: def.Quality,
		},
	}
	require.NoError(t, f.c.Create(ctx, mf))
	require.NoError(t, f.api.Get(ctx, client.ObjectKeyFromObject(mf), mf))
	return mf
}

// TestTheExecuteTaskRefusesAStaleBasis (A3.8, Review Focus 5): an approved
// plan replaces the MediaFiles it was made against, by UID and
// resourceVersion. When the replaced MediaFile changed between the inspect
// and the execute -- edited, recreated under its name, or deleted -- the
// execute refuses the plan as "stale" before it touches the disk: nothing is
// placed, nothing recycled, no MediaFile deleted, and the manager inspects
// again. Only a plan whose basis still holds places the file and recycles
// the one it replaces.
func TestTheExecuteTaskRefusesAStaleBasis(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-stale")
	bin := dataDir(t, "recycle")
	require.NoError(t, f.c.Patch(ctx, &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Namespace: f.ns, Name: f.rootFolder.Name},
	}, client.RawPatch("application/merge-patch+json", []byte(`{"spec":{"recycleBin":{"path":"`+bin+`"}}}`))))
	waitFor(t, 5*time.Second, func() bool {
		var rf catalogv1alpha1.RootFolder
		return f.c.Get(ctx, client.ObjectKeyFromObject(f.rootFolder), &rf) == nil && rf.Spec.RecycleBin.Path == bin
	})

	contentRoot := dataDir(t, "scratch")
	const title = "The.Matrix.1999.1080p.BluRay.x264-GRP"
	src := filepath.Join(contentRoot, title+".mkv")
	mustWriteSparseFile(t, src, sampleFloor)
	oldPath := filepath.Join(f.mediaRoot, "The Matrix (1999)", "The.Matrix.1999.720p.HDTV.x264-OLD.mkv")
	old := f.existingMovieFile(t, "the-matrix-old", oldPath)
	waitFor(t, 5*time.Second, func() bool {
		return f.c.Get(ctx, client.ObjectKeyFromObject(old), &catalogv1alpha1.MediaFile{}) == nil
	})

	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	r := f.movieRun(t, "stale-entry", contentRoot, title, now)
	r.decide()
	r.deliver() // the inspect
	r.decide()
	task := r.executeTask()
	require.Len(t, task.Plan.Moves, 1)
	require.Equal(t, []schema.MediaFileBasis{{
		Name: old.Name, UID: string(old.UID), ResourceVersion: old.ResourceVersion, Path: oldPath,
	}}, task.Plan.Replaces, "the plan replaces the file it was judged against, at its resourceVersion")
	dest := task.Plan.Moves[0].Dest

	untouched := func(t *testing.T) {
		t.Helper()
		assert.NoFileExists(t, dest, "nothing is placed")
		assert.FileExists(t, oldPath, "nothing is recycled")
		assert.FileExists(t, src, "the payload stays where it is")
		entries, err := os.ReadDir(bin)
		require.NoError(t, err)
		assert.Empty(t, entries, "the recycle bin is untouched")
	}
	refused := func(t *testing.T, why string) {
		t.Helper()
		rec := r.record(schema.ImportSubExecute)
		require.NotNil(t, rec)
		require.Equal(t, task.Seq, rec.Seq)
		require.NotNil(t, rec.Execute)
		assert.Equal(t, schema.ImportRefusedStale, rec.Execute.Refused)
		assert.Contains(t, rec.Failure, why)
		assert.Empty(t, rec.Execute.Placed)
		assert.Empty(t, rec.Execute.Recycled)
		assert.Empty(t, rec.Execute.Replaced)
		untouched(t)
	}

	// Edited: its resourceVersion moved between the inspect and the execute.
	require.NoError(t, f.c.Patch(ctx, old, client.RawPatch("application/merge-patch+json",
		[]byte(`{"metadata":{"labels":{"touched":"between-inspect-and-execute"}}}`))))
	r.deliver()
	refused(t, "changed since the plan")
	var still catalogv1alpha1.MediaFile
	require.NoError(t, f.api.Get(ctx, client.ObjectKeyFromObject(old), &still), "the agent deletes no MediaFile")

	// The manager reads the refusal and inspects again; nothing materialises.
	dec := r.decide()
	assert.Equal(t, lifecycle.ImportNone, dec.Verdict)
	assert.Empty(t, dec.Materialise)
	require.NotNil(t, dec.Dispatch)
	assert.Equal(t, schema.ImportSubInspect, dec.Dispatch.Phase, "a stale plan is inspected again")
	assert.Greater(t, dec.Dispatch.Seq, task.Seq)
	assert.Contains(t, dec.Summary.Message, "changed since it was made")

	// Recreated under its name: the UID differs.
	require.NoError(t, f.c.Delete(ctx, &still))
	recreated := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Namespace: f.ns, Name: old.Name},
		Spec:       old.Spec,
	}
	require.NoError(t, f.c.Create(ctx, recreated))
	task.Seq++
	require.NoError(t, r.handle(task))
	refused(t, "recreated since the plan")

	// Deleted.
	require.NoError(t, f.c.Delete(ctx, recreated))
	waitFor(t, 5*time.Second, func() bool {
		return f.api.Get(ctx, client.ObjectKeyFromObject(recreated), &catalogv1alpha1.MediaFile{}) != nil
	})
	task.Seq++
	require.NoError(t, r.handle(task))
	refused(t, "gone since the plan")

	// The control: a basis that holds is executed.
	current := f.existingMovieFile(t, "the-matrix-current", oldPath)
	task.Seq++
	task.Plan.Replaces = []schema.MediaFileBasis{{
		Name: current.Name, UID: string(current.UID), ResourceVersion: current.ResourceVersion, Path: oldPath,
	}}
	require.NoError(t, r.handle(task))
	rec := r.record(schema.ImportSubExecute)
	require.NotNil(t, rec.Execute)
	assert.Empty(t, rec.Execute.Refused, rec.Failure)
	require.Len(t, rec.Execute.Placed, 1)
	assert.Equal(t, dest, rec.Execute.Placed[0].Dest)
	assert.FileExists(t, dest)
	assert.NoFileExists(t, oldPath, "the replaced file went to the recycle bin")
	assert.Equal(t, []string{oldPath}, rec.Execute.Recycled)
	require.NoError(t, f.api.Get(ctx, client.ObjectKeyFromObject(current), &catalogv1alpha1.MediaFile{}),
		"the manager, not the agent, deletes the replaced MediaFile")
}
