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

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/import/manager/scanapply"
	"github.com/mediactl/clustarr/app/import/scanprogress"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// applyingWorker is a Worker over a fake cluster holding objs whose
// observations the manager's scan applier applies as they are published
// (A6.1: the walk reports, the applier decides and writes), with now as
// both clocks (nil: the real one).
func applyingWorker(t *testing.T, ctx context.Context, now func() time.Time, objs ...client.Object) (*Worker, client.Client, events.Bus) {
	t.Helper()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).
		WithIndex(&catalogv1alpha1.MediaFile{}, MediaFilePathIndexKey, func(o client.Object) []string {
			return []string{o.(*catalogv1alpha1.MediaFile).Spec.Path}
		}).Build()
	applier := &scanapply.Applier{Client: c, Reader: c, APIReader: c, Bus: bus, Now: now}
	return &Worker{Client: c, Bus: ApplyingBus(bus, applier), Clock: now}, c, bus
}

// excludedFixture is applyingWorker over root, with a bus whose exclusion
// index names provider/id.
func excludedFixture(t *testing.T, ctx context.Context, provider, id string, root *catalogv1alpha1.RootFolder) (*Worker, client.Client, events.Bus) {
	t.Helper()
	w, c, bus := applyingWorker(t, ctx, nil, root)
	data, err := events.ExclusionEntry{Namespace: root.Namespace, Name: "excluded", Kind: "item"}.Encode()
	require.NoError(t, err)
	_, err = bus.KV(events.BucketImportExclusions).Put(ctx, events.ExclusionKey(provider, id), data)
	require.NoError(t, err)
	return w, c, bus
}

// appliedTally is the scan applier's tally for scan: what it decided.
func appliedTally(t *testing.T, ctx context.Context, bus events.Bus, scan *catalogv1alpha1.LibraryScan) scanprogress.Progress {
	t.Helper()
	entry, err := bus.KV(events.BucketProgress).Get(ctx, scanprogress.AppliedKey(string(scan.UID)))
	require.NoError(t, err, "the scan applier recorded no tally")
	p, err := scanprogress.DecodeProgress(entry.Value)
	require.NoError(t, err)
	return p
}

// plant writes a one-byte file at path and returns its FileInfo.
func plant(t *testing.T, path string) os.FileInfo {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info
}

// A folder whose TheTVDB id is excluded -- a series deleted with its files
// kept and "Add import list exclusion" ticked -- is recorded unmatched and
// no Series is created. Since A6.1 the walk proposes the series and the
// scan applier refuses it, listing the file in its own tally.
func TestCreateSeriesSkipsAnExcludedFolder(t *testing.T) {
	ctx := context.Background()
	rootDir := t.TempDir()
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Namespace: "tv", Name: "shows"},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: rootDir, Kind: catalogv1alpha1.RootFolderKindSeries,
			Defaults: catalogv1alpha1.RootDefaults{QualityProfileRef: "hd"},
		},
	}
	w, c, bus := excludedFixture(t, ctx, catalogv1alpha1.ExclusionIDKeyTVDB, "393189", rf)
	st := &scanState{
		scan: &catalogv1alpha1.LibraryScan{ObjectMeta: metav1.ObjectMeta{Namespace: "tv", Name: "scan", UID: "scan-uid"}},
		root: rf,
	}
	path := filepath.Join(rootDir, "Andor (2022) {tvdb-393189}", "S01E01.mkv")
	_, err := w.createSeries(ctx, st, path, plant(t, path), &SeriesCandidate{TvdbID: 393189, Title: "Andor"})
	require.NoError(t, err)
	var list catalogv1alpha1.SeriesList
	require.NoError(t, c.List(ctx, &list))
	assert.Empty(t, list.Items)
	unmatched := appliedTally(t, ctx, bus, st.scan).Unmatched
	require.Len(t, unmatched, 1)
	assert.Contains(t, unmatched[0].Reason, "excluded")
}

// A movie file whose folder names an excluded TMDB id is recorded
// unmatched and no Movie is created (final review, finding 5: the movie
// branch of the exclusion check).
func TestAMovieFileWithAnExcludedTMDBIDCreatesNothing(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Namespace: "films", Name: "movies"},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: root, Kind: catalogv1alpha1.RootFolderKindMovie,
			Defaults: catalogv1alpha1.RootDefaults{QualityProfileRef: "hd"},
		},
	}
	w, c, bus := excludedFixture(t, ctx, catalogv1alpha1.ExclusionIDKeyTMDB, "949", rf)
	path := filepath.Join(root, "Heat (1995) {tmdb-949}", "Heat (1995).mkv")
	info := plant(t, path)
	st := &scanState{
		scan: &catalogv1alpha1.LibraryScan{ObjectMeta: metav1.ObjectMeta{Namespace: "films", Name: "scan", UID: "scan-uid"}},
		root: rf,
	}
	// The walk makes every file its probe (visit); this worker probes nothing.
	require.NoError(t, w.attributeMediaFile(ctx, st, path, info, w.probeFor(path, nil)))
	var list catalogv1alpha1.MovieList
	require.NoError(t, c.List(ctx, &list))
	assert.Empty(t, list.Items)
	unmatched := appliedTally(t, ctx, bus, st.scan).Unmatched
	require.Len(t, unmatched, 1)
	assert.Contains(t, unmatched[0].Reason, "excluded")
}
