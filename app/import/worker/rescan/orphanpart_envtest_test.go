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

package rescan_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/rescan"
)

// A transcode attempt killed mid-encode (an OOM kill, a lost node) leaves
// its <stem>.part-<uid8>-<attempt>.<ext> beside the media for good. The
// rescan removes one unwritten for OrphanPartAge whose job is no live
// TranscodeJob, and nothing else: a live job's part, a young part, a
// torrent's .part, the generic form and a non-part file all stay.
func TestARescanRemovesAnAbandonedTranscodeAttemptsPartFile(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-orphan-parts", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
	dir := filepath.Join(f.root, "Movie (2020) [tmdbid-1]")

	uid8 := func(name string) string {
		var j transcodev1alpha1.TranscodeJob
		require.NoError(t, f.c.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: name}, &j))
		return string(j.UID)[:8]
	}
	src := "/data/movies/Movie (2020) [tmdbid-1]/Movie (2020).mkv"
	finishedJob(t, ctx, f, "failed-job", "mf", src, "", transcodev1alpha1.TranscodeJobPhaseFailed)
	finishedJob(t, ctx, f, "running-job", "mf", src, "", transcodev1alpha1.TranscodeJobPhaseRunning)

	long := time.Now().Add(-rescan.OrphanPartAge - time.Hour)
	plant := func(name string, mod time.Time) string {
		path := filepath.Join(dir, name)
		mustWriteFile(t, path, 64)
		require.NoError(t, os.Chtimes(path, mod, mod))
		return path
	}
	gone := []string{
		plant("Movie (2020).part-"+uid8("failed-job")+"-1.mkv", long), // its job failed
		plant("Movie (2020).part-deadbeef-2.mkv", long),               // its job is gone
	}
	kept := []string{
		plant("Movie (2020).part-"+uid8("running-job")+"-1.mkv", long), // its job is live
		plant("Movie (2020).part-cafef00d-1.mkv", time.Now().Add(-time.Hour)),
		plant("Movie (2020).mkv.part", long),
		plant("Movie (2020).part.mkv", long),
		plant("notes.txt", long),
	}

	msg := newFakeMessage(t, f.task(false))
	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, msg))
	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	require.True(t, got.Done)

	for _, p := range gone {
		_, err := os.Lstat(p)
		assert.ErrorIsf(t, err, os.ErrNotExist, "%s is an abandoned attempt's part", filepath.Base(p))
	}
	for _, p := range kept {
		_, err := os.Lstat(p)
		assert.NoErrorf(t, err, "%s must stay", filepath.Base(p))
	}
}
