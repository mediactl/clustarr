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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// A folder whose TheTVDB id is excluded -- a series deleted with its files
// kept and "Add import list exclusion" ticked -- is recorded unmatched and
// no Series is created.
func TestCreateSeriesSkipsAnExcludedFolder(t *testing.T) {
	ctx := context.Background()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	data, err := events.ExclusionEntry{Namespace: "tv", Name: "andor", Kind: "series"}.Encode()
	require.NoError(t, err)
	_, err = bus.KV(events.BucketImportExclusions).Put(ctx, events.ExclusionKey(catalogv1alpha1.ExclusionIDKeyTVDB, "393189"), data)
	require.NoError(t, err)

	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	w := &Worker{Client: c, Bus: bus}
	st := &scanState{
		scan: &catalogv1alpha1.LibraryScan{ObjectMeta: metav1.ObjectMeta{Namespace: "tv", Name: "scan"}},
		root: &catalogv1alpha1.RootFolder{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tv", Name: "shows"},
			Spec:       catalogv1alpha1.RootFolderSpec{Defaults: catalogv1alpha1.RootDefaults{QualityProfileRef: "hd"}},
		},
	}
	got, err := w.createSeries(ctx, st, "Andor (2022) {tvdb-393189}/S01E01.mkv", &SeriesCandidate{TvdbID: 393189, Title: "Andor"})
	require.NoError(t, err)
	assert.Nil(t, got)
	var list catalogv1alpha1.SeriesList
	require.NoError(t, c.List(ctx, &list))
	assert.Empty(t, list.Items)
	require.Len(t, st.progress.Unmatched, 1)
	assert.Contains(t, st.progress.Unmatched[0].Reason, "excluded")
}

// A movie file whose folder names an excluded TMDB id is recorded
// unmatched and no Movie is created (final review, finding 5: the movie
// branch of the exclusion check).
func TestAMovieFileWithAnExcludedTMDBIDCreatesNothing(t *testing.T) {
	ctx := context.Background()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	data, err := events.ExclusionEntry{Namespace: "films", Name: "heat", Kind: "movie"}.Encode()
	require.NoError(t, err)
	_, err = bus.KV(events.BucketImportExclusions).Put(ctx, events.ExclusionKey(catalogv1alpha1.ExclusionIDKeyTMDB, "949"), data)
	require.NoError(t, err)

	root := t.TempDir()
	path := filepath.Join(root, "Heat (1995) {tmdb-949}", "Heat (1995).mkv")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	info, err := os.Stat(path)
	require.NoError(t, err)

	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithIndex(&catalogv1alpha1.MediaFile{}, MediaFilePathIndexKey, func(o client.Object) []string {
			return []string{o.(*catalogv1alpha1.MediaFile).Spec.Path}
		}).Build()
	w := &Worker{Client: c, Bus: bus}
	st := &scanState{
		scan: &catalogv1alpha1.LibraryScan{ObjectMeta: metav1.ObjectMeta{Namespace: "films", Name: "scan"}},
		root: &catalogv1alpha1.RootFolder{ObjectMeta: metav1.ObjectMeta{Namespace: "films", Name: "movies"},
			Spec: catalogv1alpha1.RootFolderSpec{Path: root, Kind: catalogv1alpha1.RootFolderKindMovie,
				Defaults: catalogv1alpha1.RootDefaults{QualityProfileRef: "hd"}}},
	}
	require.NoError(t, w.attributeMediaFile(ctx, st, path, info, nil))
	var list catalogv1alpha1.MovieList
	require.NoError(t, c.List(ctx, &list))
	assert.Empty(t, list.Items)
	require.Len(t, st.progress.Unmatched, 1)
	assert.Contains(t, st.progress.Unmatched[0].Reason, "excluded")
}
