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

package folderscan_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/folderscan"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// osDirs reads straight from the filesystem, as the remediation loop's
// executor does on its pool.
type osDirs struct{ err error }

func (o osDirs) ReadDir(_ context.Context, dir string) ([]fs.DirEntry, error) {
	if o.err != nil {
		return nil, o.err
	}
	return os.ReadDir(dir)
}

// The probe maps the logical /data path onto its mount, and a folder holds
// files when it lists anything that is not hidden: a season folder counts,
// a missing folder, an empty one or one holding only dotfiles does not, and
// a read that fails for another reason is an error, never "nothing there".
func TestProbeHoldsFiles(t *testing.T) {
	data := t.TempDir()
	mk := func(rel string) {
		require.NoError(t, os.MkdirAll(filepath.Join(data, rel), 0o755))
	}
	mk("media/tv/Empty")
	mk("media/tv/Hidden/.clustarr")
	require.NoError(t, os.WriteFile(filepath.Join(data, "media/tv/Hidden/.DS_Store"), nil, 0o644))
	mk("media/tv/Seasons/Season 01")
	mk("media/tv/Flat")
	require.NoError(t, os.WriteFile(filepath.Join(data, "media/tv/Flat/Show - S01E01.mkv"), nil, 0o644))

	p := folderscan.Probe{IO: osDirs{}, DataDir: data}
	for path, want := range map[string]bool{
		"/data/media/tv/Missing": false,
		"/data/media/tv/Empty":   false,
		"/data/media/tv/Hidden":  false,
		"/data/media/tv/Seasons": true,
		"/data/media/tv/Flat":    true,
	} {
		got, err := p.HoldsFiles(context.Background(), path)
		require.NoError(t, err, path)
		assert.Equal(t, want, got, path)
	}

	_, err := folderscan.Probe{IO: osDirs{err: errors.New("breaker open")}, DataDir: data}.HoldsFiles(context.Background(), "/data/media/tv/Flat")
	assert.Error(t, err, "a failed read is not an empty folder")
}

// fakeFolders answers HoldsFiles from a map and counts the calls.
type fakeFolders struct {
	holds map[string]bool
	err   error
	calls int
}

func (f *fakeFolders) HoldsFiles(_ context.Context, path string) (bool, error) {
	f.calls++
	return f.holds[path], f.err
}

func series(conditions ...metav1.Condition) *catalogv1alpha1.Series {
	return &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tv", Name: "show-81189", UID: "series-uid", Generation: 1},
		Status:     catalogv1alpha1.SeriesStatus{Conditions: conditions},
	}
}

var root = &catalogv1alpha1.RootFolder{
	ObjectMeta: metav1.ObjectMeta{Namespace: "tv", Name: "tv"},
	Spec:       catalogv1alpha1.RootFolderSpec{Path: "/data/media/tv", Kind: catalogv1alpha1.RootFolderKindSeries},
}

const folder = "/data/media/tv/Breaking Bad (2008) [tvdbid-81189]"

func scans(t *testing.T, c client.Client) []catalogv1alpha1.LibraryScan {
	t.Helper()
	var list catalogv1alpha1.LibraryScanList
	require.NoError(t, c.List(context.Background(), &list, client.InNamespace("tv")))
	return list.Items
}

// TestPassScansTheFolderOnceItsFirstChildrenSettle is smoke finding O8: a
// series a scan created got its files only at the next scan. The pass that
// first lists its episodes owes its folder a scan, asks for it once the new
// Episodes have settled into the import agent's cache, and records it, so it
// is never asked for again.
func TestPassScansTheFolderOnceItsFirstChildrenSettle(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	folders := &fakeFolders{holds: map[string]bool{folder: true}}
	s := series()
	now := time.Now()
	req := folderscan.Request{Item: s, Root: root, Path: folder, HasChildren: true, Children: "episodes",
		FieldManager: k8s.ManagerCatalog, Now: now}

	// The pass that creates the first episodes: owed, settling.
	conds := append([]metav1.Condition(nil), s.Status.Conditions...)
	out := folderscan.Pass(ctx, c, folders, req, &conds)
	cond := k8s.FindCondition(conds, catalogv1alpha1.SeriesConditionFolderScanned)
	require.NotNil(t, cond, "the first episodes record the scan they owe")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, folderscan.ReasonSettling, cond.Reason)
	assert.Equal(t, folderscan.SettleAfter, out.Retry)
	assert.Empty(t, out.Scan)
	assert.Zero(t, folders.calls, "nothing is read while the episodes settle")
	assert.Empty(t, scans(t, c))

	// A pass before the settle is up waits out the rest.
	req.HadChildren = true // the status now records them
	req.Now = cond.LastTransitionTime.Add(10 * time.Second)
	out = folderscan.Pass(ctx, c, folders, req, &conds)
	assert.Equal(t, 20*time.Second, out.Retry)
	assert.Empty(t, scans(t, c))

	// Once it is up: one incremental scan of the series' own folder.
	req.Now = cond.LastTransitionTime.Add(folderscan.SettleAfter)
	out = folderscan.Pass(ctx, c, folders, req, &conds)
	assert.Equal(t, folderscan.ScanName(s), out.Scan)
	assert.Zero(t, out.Retry)
	got := scans(t, c)
	require.Len(t, got, 1)
	assert.Equal(t, folderscan.ScanName(s), got[0].Name)
	assert.Equal(t, catalogv1alpha1.LibraryScanSpec{
		RootFolderRef: "tv", Subpath: "Breaking Bad (2008) [tvdbid-81189]", Mode: catalogv1alpha1.ScanModeIncremental,
	}, got[0].Spec)
	assert.Equal(t, "tv", got[0].Labels[folderscan.LabelRootFolder])
	cond = k8s.FindCondition(conds, catalogv1alpha1.SeriesConditionFolderScanned)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, folderscan.ReasonScanRequested, cond.Reason)
	assert.Contains(t, cond.Message, got[0].Name)

	// Done for good: no read, no second scan.
	calls := folders.calls
	out = folderscan.Pass(ctx, c, folders, req, &conds)
	assert.Equal(t, folderscan.Outcome{}, out)
	assert.Equal(t, calls, folders.calls)
	assert.Len(t, scans(t, c), 1)
}

// An item whose status already recorded its children -- every series when
// this shipped -- owes nothing, and neither does one with no children yet.
func TestPassOwesNothingWithoutAFirstListing(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	folders := &fakeFolders{holds: map[string]bool{folder: true}}
	for name, req := range map[string]folderscan.Request{
		"children recorded before": {HadChildren: true, HasChildren: true},
		"no children yet":          {},
	} {
		req.Item, req.Root, req.Path, req.Now = series(), root, folder, time.Now()
		var conds []metav1.Condition
		out := folderscan.Pass(context.Background(), c, folders, req, &conds)
		assert.Equal(t, folderscan.Outcome{}, out, name)
		assert.Empty(t, conds, name)
	}
	assert.Zero(t, folders.calls)
	assert.Empty(t, scans(t, c))
	assert.Equal(t, folderscan.Outcome{}, folderscan.Pass(context.Background(), c, nil,
		folderscan.Request{Item: series(), HasChildren: true}, new([]metav1.Condition)), "no prober, no scan")
}

// interceptCreate fails every create, as an apiserver that cannot be reached.
var interceptCreate = interceptor.Funcs{
	Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
		return errors.New("connection refused")
	},
}

// settled is a FolderScanned condition whose settle is long over.
func settled() metav1.Condition {
	return metav1.Condition{Type: catalogv1alpha1.SeriesConditionFolderScanned, Status: metav1.ConditionFalse,
		Reason: folderscan.ReasonSettling, LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Hour))}
}

// A folder that is missing or empty is settled without a scan: there is
// nothing for one to find, and a download's import brings any file later.
func TestPassScansNothingWhenTheFolderHoldsNothing(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	folders := &fakeFolders{holds: map[string]bool{}}
	conds := []metav1.Condition{settled()}
	out := folderscan.Pass(context.Background(), c, folders, folderscan.Request{
		Item: series(), Root: root, Path: folder, HadChildren: true, HasChildren: true, Now: time.Now(),
	}, &conds)
	assert.Equal(t, folderscan.Outcome{}, out)
	assert.Empty(t, scans(t, c))
	cond := k8s.FindCondition(conds, catalogv1alpha1.SeriesConditionFolderScanned)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, folderscan.ReasonNothingOnDisk, cond.Reason)
}

// What cannot be settled now stays owed: a folder not known yet (no
// metadata, so no status.path) waits for the metadata write that wakes the
// item, and a read or a create that fails is retried. A folder outside its
// root folder, which no scan of that root can reach, is settled, not retried.
func TestPassRetriesWhatItCannotSettle(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	for name, tc := range map[string]struct {
		root    *catalogv1alpha1.RootFolder
		path    string
		folders *fakeFolders
		retry   time.Duration
	}{
		"no path yet": {root: root, folders: &fakeFolders{}},
		"no root yet": {path: folder, folders: &fakeFolders{}},
		"the read failed": {root: root, path: folder, retry: folderscan.RetryAfter,
			folders: &fakeFolders{err: errors.New("remediation: /data I/O call timed out")}},
		"the create failed": {root: root, path: folder, retry: folderscan.RetryAfter,
			folders: &fakeFolders{holds: map[string]bool{folder: true}}},
	} {
		t.Run(name, func(t *testing.T) {
			conds := []metav1.Condition{settled()}
			cl := c
			if name == "the create failed" {
				// A create that fails for another reason than AlreadyExists.
				cl = fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithInterceptorFuncs(interceptCreate).Build()
			}
			out := folderscan.Pass(ctx, cl, tc.folders, folderscan.Request{
				Item: series(), Root: tc.root, Path: tc.path, HadChildren: true, HasChildren: true, Now: time.Now(),
			}, &conds)
			assert.Equal(t, tc.retry, out.Retry)
			assert.Empty(t, out.Scan)
			cond := k8s.FindCondition(conds, catalogv1alpha1.SeriesConditionFolderScanned)
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, folderscan.ReasonPending, cond.Reason)
		})
	}

	conds := []metav1.Condition{settled()}
	folders := &fakeFolders{holds: map[string]bool{"/data/media/other/Show": true}}
	out := folderscan.Pass(ctx, c, folders, folderscan.Request{
		Item: series(), Root: root, Path: "/data/media/other/Show", HadChildren: true, HasChildren: true, Now: time.Now(),
	}, &conds)
	assert.Equal(t, folderscan.Outcome{}, out, "a folder outside the root folder is never retried")
	assert.Equal(t, metav1.ConditionTrue, k8s.FindCondition(conds, catalogv1alpha1.SeriesConditionFolderScanned).Status)
	assert.Empty(t, scans(t, c))
}

// A pass that repeats after its status apply was lost finds the scan it
// created and records it, rather than creating a second.
func TestPassFindsTheScanItAlreadyCreated(t *testing.T) {
	s := series()
	existing := &catalogv1alpha1.LibraryScan{ObjectMeta: metav1.ObjectMeta{Namespace: "tv", Name: folderscan.ScanName(s)},
		Spec: catalogv1alpha1.LibraryScanSpec{RootFolderRef: "tv"}}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(existing).Build()
	conds := []metav1.Condition{settled()}
	out := folderscan.Pass(context.Background(), c, &fakeFolders{holds: map[string]bool{folder: true}}, folderscan.Request{
		Item: s, Root: root, Path: folder, HadChildren: true, HasChildren: true, Now: time.Now(),
	}, &conds)
	assert.Equal(t, folderscan.ScanName(s), out.Scan)
	assert.Len(t, scans(t, c), 1)
	var got catalogv1alpha1.LibraryScan
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "tv", Name: folderscan.ScanName(s)}, &got))
	assert.Equal(t, metav1.ConditionTrue, k8s.FindCondition(conds, catalogv1alpha1.SeriesConditionFolderScanned).Status)
}
