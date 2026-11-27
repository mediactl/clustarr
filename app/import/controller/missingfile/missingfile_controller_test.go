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

package missingfile

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/pkg/k8s"
)

const ns = "media"

var marked = time.Date(2026, 11, 27, 6, 34, 20, 0, time.UTC)

// file is a MediaFile at path whose file the reconciler marked missing at
// since; the zero time is a file that is there.
func file(name, path string, since time.Time) *catalogv1alpha1.MediaFile {
	mf := &catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	mf.Spec.Path = path
	if !since.IsZero() {
		mf.Status.Conditions = []metav1.Condition{{
			Type: catalogv1alpha1.MediaFileConditionReady, Status: metav1.ConditionFalse, Reason: "FileMissing",
			LastTransitionTime: metav1.NewTime(since),
		}}
	}
	return mf
}

func rootFolder(name, path string) *catalogv1alpha1.RootFolder {
	return &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: path, Kind: catalogv1alpha1.RootFolderKindSeries},
	}
}

// at is the controller over objs with its clock at now.
func at(now time.Time, objs ...client.Object) *Reconciler {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithIndex(&catalogv1alpha1.MediaFile{}, MarkedIndex, markedValues).
		WithObjects(objs...).Build()
	return &Reconciler{Client: c, Clock: func() time.Time { return now }}
}

func pass(t *testing.T, r *Reconciler, name string) reconcile.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
	require.NoError(t, err)
	return res
}

func scans(t *testing.T, r *Reconciler) []catalogv1alpha1.LibraryScan {
	t.Helper()
	var l catalogv1alpha1.LibraryScanList
	require.NoError(t, r.Client.List(context.Background(), &l, client.InNamespace(ns)))
	return l.Items
}

// The second half of the gap of 2026-11-27: once a file was marked missing
// its record still waited for a scan nobody had asked for, since no root
// folder on talos has a rescan schedule, and its episode went on counting a
// file it did not have. The scan is asked for when the grace and a margin
// have passed, of the file's own folder, once.
func TestAFileMissingPastTheGraceGetsAScanOfItsFolder(t *testing.T) {
	season := "/data/media/anime/Gosick (2011)/Season 01"
	objs := func() []client.Object {
		return []client.Object{
			rootFolder("anime", "/data/media/anime"), rootFolder("tv", "/data/media/tv"),
			file("e22", season+"/S01E22.mkv", marked),
			// Marked in the same minute: the folder went together.
			file("e23", season+"/S01E23.mkv", marked.Add(30*time.Second)),
			file("present", season+"/S01E21.mkv", time.Time{}),
		}
	}
	due := marked.Add(mediafilespec.MissingGrace + Margin)

	r := at(due.Add(-time.Minute), objs()...)
	assert.Equal(t, time.Minute, pass(t, r, "e22").RequeueAfter, "inside the grace it waits for its end")
	assert.Empty(t, scans(t, r))

	r = at(due, objs()...)
	assert.Zero(t, pass(t, r, "e22").RequeueAfter, "asked once: nothing to come back for")
	got := scans(t, r)
	require.Len(t, got, 1)
	assert.Equal(t, catalogv1alpha1.LibraryScanSpec{
		RootFolderRef: "anime", Subpath: "Gosick (2011)/Season 01", Mode: catalogv1alpha1.ScanModeIncremental,
	}, got[0].Spec)
	assert.Equal(t, map[string]string{labelRootFolder: "anime"}, got[0].Labels)

	// Its neighbour comes due half a minute later and is past the grace by
	// the time the first one's scan runs, so it asks for the same scan.
	r.Clock = func() time.Time { return due.Add(30 * time.Second) }
	pass(t, r, "e23")
	pass(t, r, "e22")
	assert.Len(t, scans(t, r), 1)

	assert.Zero(t, pass(t, r, "present").RequeueAfter)
	assert.Zero(t, pass(t, r, "no-such-file").RequeueAfter)
	assert.Len(t, scans(t, r), 1, "a file that is there, or a record that is gone, asks for nothing")
}

// A share that comes back empty marks every file missing, and a scan of
// each folder would remove every record, each item then wanted and
// downloaded again. Past MassLimit files at once nothing is asked for; a
// person's scan still removes them.
func TestNoScanIsAskedForWhileALibrarysWorthOfFilesIsMissing(t *testing.T) {
	objs := []client.Object{rootFolder("anime", "/data/media/anime")}
	for i := range MassLimit + 1 {
		objs = append(objs, file(fmt.Sprintf("f%03d", i), fmt.Sprintf("/data/media/anime/Show %03d/S01E01.mkv", i), marked))
	}
	r := at(marked.Add(time.Hour), objs...)
	assert.Equal(t, HoldRecheck, pass(t, r, "f000").RequeueAfter)
	assert.Empty(t, scans(t, r))

	// One fewer is a person tidying up.
	r = at(marked.Add(time.Hour), objs[:len(objs)-1]...)
	assert.Zero(t, pass(t, r, "f000").RequeueAfter)
	assert.Len(t, scans(t, r), 1)
}

func TestFolder(t *testing.T) {
	roots := []catalogv1alpha1.RootFolder{
		*rootFolder("tv", "/data/media/tv"), *rootFolder("anime", "/data/media/tv/anime"), *rootFolder("unset", ""),
	}
	for _, tc := range []struct{ path, root, sub string }{
		{"/data/media/tv/Andor/Season 01/S01E01.mkv", "tv", "Andor/Season 01"},
		{"/data/media/tv/anime/Gosick/S01E01.mkv", "anime", "Gosick"},
		// Directly under the root folder: the one file, never the library.
		{"/data/media/tv/stray.mkv", "tv", "stray.mkv"},
		{"/data/media/tvshows/Andor/S01E01.mkv", "", ""},
		{"/data/media/movies/Heat (1995)/Heat.mkv", "", ""},
	} {
		root, sub := Folder(roots, tc.path)
		if tc.root == "" {
			assert.Nil(t, root, tc.path)
			continue
		}
		require.NotNil(t, root, tc.path)
		assert.Equal(t, tc.root, root.Name, tc.path)
		assert.Equal(t, tc.sub, sub, tc.path)
	}
}

// A scan is named by the minute its file was marked: files marked within
// the Margin share one, and a later spell in the same folder gets its own.
func TestScanName(t *testing.T) {
	a := ScanName("anime", "Gosick/Season 01", marked)
	assert.Equal(t, a, ScanName("anime", "Gosick/Season 01", marked.Add(30*time.Second)))
	assert.NotEqual(t, a, ScanName("anime", "Gosick/Season 01", marked.Add(time.Minute)))
	assert.NotEqual(t, a, ScanName("anime", "Gosick/Season 02", marked))
	assert.NotEqual(t, a, ScanName("tv", "Gosick/Season 01", marked))
}
