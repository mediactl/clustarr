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

package rescan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

var pruneNow = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// recordFor is a MediaFile for path, flagged FileMissing missingFor ago by
// the MediaFile reconciler; zero is never flagged.
func recordFor(name, path string, missingFor time.Duration) *catalogv1alpha1.MediaFile {
	mf := &catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: name}}
	mf.Spec.Path = path
	mf.Spec.MediaRef = commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "ep"}
	if missingFor > 0 {
		mf.Status.Conditions = []metav1.Condition{{
			Type: catalogv1alpha1.MediaFileConditionReady, Status: metav1.ConditionFalse, Reason: "FileMissing",
			LastTransitionTime: metav1.NewTime(pruneNow.Add(-missingFor)),
		}}
	}
	return mf
}

func pruneScan(t *testing.T, root, subpath string, objs ...client.Object) (*Worker, *scanState) {
	t.Helper()
	w := &Worker{
		Client: fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).Build(),
		Clock:  func() time.Time { return pruneNow },
	}
	st := &scanState{
		task: schema.ScanTask{Path: filepath.Join(root, subpath)},
		scan: &catalogv1alpha1.LibraryScan{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "tv-x"}},
		root: &catalogv1alpha1.RootFolder{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "tv"},
			Spec:       catalogv1alpha1.RootFolderSpec{Path: root, Kind: catalogv1alpha1.RootFolderKindSeries},
		},
	}
	return w, st
}

func exists(t *testing.T, w *Worker, name string) bool {
	t.Helper()
	err := w.Client.Get(context.Background(), client.ObjectKey{Namespace: "media", Name: name}, &catalogv1alpha1.MediaFile{})
	if apierrors.IsNotFound(err) {
		return false
	}
	require.NoError(t, err)
	return true
}

// A folder renamed "Season 01" -> "Season 1" left a MediaFile at every old
// path (832 on the owner's library, 2026-10-01): the scan removes the
// record of a file gone from disk once the MediaFile reconciler has seen it
// missing for the grace period, as Sonarr's rescan removes a missing
// episode file. It never removes one on a guess.
func TestTheScanRemovesRecordsOfFilesGoneFromDisk(t *testing.T) {
	root := t.TempDir()
	show := filepath.Join(root, "Seinfeld (1989)")
	require.NoError(t, os.MkdirAll(filepath.Join(show, "Season 1"), 0o755))
	here := filepath.Join(show, "Season 1", "S01E01.mkv")
	require.NoError(t, os.WriteFile(here, []byte("x"), 0o644))
	gone := filepath.Join(show, "Season 01", "S01E01.mkv")

	w, st := pruneScan(t, root, "",
		recordFor("gone", gone, time.Hour),
		recordFor("gone-unflagged", gone+".2", 0),
		recordFor("gone-just-now", gone+".3", 2*time.Minute),
		recordFor("present-but-flagged", here, time.Hour),
	)
	require.NoError(t, w.prunePass(context.Background(), st))
	assert.False(t, exists(t, w, "gone"), "gone from disk and seen missing past the grace")
	assert.True(t, exists(t, w, "gone-unflagged"), "the reconciler has not seen it missing")
	assert.True(t, exists(t, w, "gone-just-now"), "within the grace: a share's blip")
	assert.True(t, exists(t, w, "present-but-flagged"), "on disk now")
	assert.EqualValues(t, 1, st.progress.FilesRemoved)
}

// A scan of one folder removes only records under it.
func TestASubpathScanRemovesOnlyUnderItsPath(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Andor"), 0o755))
	w, st := pruneScan(t, root, "Andor",
		recordFor("andor", filepath.Join(root, "Andor", "gone.mkv"), time.Hour),
		recordFor("andorian", filepath.Join(root, "Andorian", "gone.mkv"), time.Hour),
		recordFor("elsewhere", filepath.Join(root, "Seinfeld", "gone.mkv"), time.Hour),
	)
	require.NoError(t, w.prunePass(context.Background(), st))
	assert.False(t, exists(t, w, "andor"))
	assert.True(t, exists(t, w, "andorian"), "a sibling folder sharing the prefix")
	assert.True(t, exists(t, w, "elsewhere"))
}

// A manual assignment names one file and removes nothing; a root that is
// itself gone (an unmounted share) is not a library whose files all went.
func TestNoRemovalOnAManualAssignOrAMissingRoot(t *testing.T) {
	root := t.TempDir()
	w, st := pruneScan(t, root, "", recordFor("gone", filepath.Join(root, "x", "gone.mkv"), time.Hour))
	st.manual = &manualAssign{}
	require.NoError(t, w.prunePass(context.Background(), st))
	assert.True(t, exists(t, w, "gone"))

	unmounted := filepath.Join(t.TempDir(), "unmounted")
	w, st = pruneScan(t, unmounted, "", recordFor("gone", filepath.Join(unmounted, "x", "gone.mkv"), time.Hour))
	require.NoError(t, w.prunePass(context.Background(), st))
	assert.True(t, exists(t, w, "gone"))
}
