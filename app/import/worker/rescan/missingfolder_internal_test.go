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

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// missingFolderScan is a scan of subpath under a books root at root.
func missingFolderScan(t *testing.T, root, subpath string) (*Worker, *scanState, events.Bus) {
	t.Helper()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(context.Background(), events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	w := &Worker{Client: fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build(), Bus: bus}
	st := &scanState{
		task: schema.ScanTask{Path: filepath.Join(root, subpath)},
		scan: &catalogv1alpha1.LibraryScan{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "books-t6tsm", UID: "scan-uid"}},
		root: &catalogv1alpha1.RootFolder{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "books"},
			Spec:       catalogv1alpha1.RootFolderSpec{Path: root, Kind: catalogv1alpha1.RootFolderKindBook},
		},
	}
	return w, st, bus
}

func reportedProgress(t *testing.T, bus events.Bus) Progress {
	t.Helper()
	e, err := bus.KV(events.BucketProgress).Get(context.Background(), ProgressKey("scan-uid"))
	require.NoError(t, err)
	p, err := DecodeProgress(e.Value)
	require.NoError(t, err)
	return p
}

// TestAScanOfAFolderNotYetOnDiskFinishesEmpty: Rescan on an author added
// through Add New, before anything was downloaded, named a folder that did
// not exist yet; the walk's lstat failed on every delivery and the scan was
// dead-lettered after four (2026-09-30). As Sonarr's RescanSeries does for
// "Series folder doesn't exist", it finishes with nothing found.
func TestAScanOfAFolderNotYetOnDiskFinishesEmpty(t *testing.T) {
	w, st, bus := missingFolderScan(t, t.TempDir(), "Fyodor Dostoyevsky")

	handled, err := w.scanMissingFolder(context.Background(), st)
	require.NoError(t, err)
	require.True(t, handled)
	p := reportedProgress(t, bus)
	require.True(t, p.Done)
	require.Empty(t, p.Error, "a folder not yet created is not a failure")
	require.Zero(t, p.FilesSeen)
}

// TestAManualAssignOfAVanishedFileIsRefused: a manual assignment names one
// file; one gone since it was listed is refused at once, not retried.
func TestAManualAssignOfAVanishedFileIsRefused(t *testing.T) {
	w, st, bus := missingFolderScan(t, t.TempDir(), "Albert Camus/The Stranger.epub")
	st.manual = &manualAssign{}

	handled, err := w.scanMissingFolder(context.Background(), st)
	require.NoError(t, err)
	require.True(t, handled)
	p := reportedProgress(t, bus)
	require.True(t, p.Done)
	require.Contains(t, p.Error, "Albert Camus/The Stranger.epub")
	require.Contains(t, p.Error, "does not exist")
}

// TestAMissingRootIsNotAnEmptyScan: a root folder that is itself gone (an
// unmounted share) must not read as an empty library -- the scan goes on
// to fail as before rather than report nothing found.
func TestAMissingRootIsNotAnEmptyScan(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "unmounted")
	w, st, _ := missingFolderScan(t, gone, "Fyodor Dostoyevsky")
	handled, err := w.scanMissingFolder(context.Background(), st)
	require.NoError(t, err)
	require.False(t, handled)

	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "Albert Camus"), 0o755))
	w, st, _ = missingFolderScan(t, root, "Albert Camus")
	handled, err = w.scanMissingFolder(context.Background(), st)
	require.NoError(t, err)
	require.False(t, handled, "a folder that exists is walked as usual")

	w, st, _ = missingFolderScan(t, root, "")
	handled, err = w.scanMissingFolder(context.Background(), st)
	require.NoError(t, err)
	require.False(t, handled, "a whole-root scan is never an empty one")
}
