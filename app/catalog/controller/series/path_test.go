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

package series_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
)

func jellyfinRoot(path string) *catalogv1alpha1.RootFolder {
	return &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "tv", Namespace: "default"},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: path, Kind: catalogv1alpha1.RootFolderKindSeries,
			Naming: catalogv1alpha1.NamingSpec{Dialect: catalogv1alpha1.NamingDialectJellyfin},
		},
	}
}

// TestPath's expected folder literal is the real Jellyfin preset output for
// SeriesFolder, confirmed against test/data/naming/dialects.json's golden
// ("The Series Title! (2010) [tvdbid-153021]") rather than guessed.
func TestPath(t *testing.T) {
	root := jellyfinRoot("/data/media/tv")
	ctx := naming.Context{Kind: commonv1.MediaKindSeries, SeriesTitle: "The Series Title!", SeriesYear: 2010, TvdbID: "153021"}

	got, err := series.Path(root, nil, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/tv/The Series Title! (2010) [tvdbid-153021]", got)

	override := "The Series Title (Extended)"
	got, err = series.Path(root, &override, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/tv/The Series Title (Extended)", got)

	parent := ".."
	_, err = series.Path(root, &parent, ctx)
	require.ErrorIs(t, err, naming.ErrUnsafeComponent, "a spec.folder naming the root's parent is refused, never the parent (path.Join had read it as one)")
}

// TestPathAgreesWithTheImport: the folder the controller writes is the one
// the import places the series' episode files under
// (catalogctx.EpisodeFilePath, the season folder between), byte for byte,
// both rendered from one Series through the import's own context -- over a
// plain title, "What If...?" (a trailing dot behind a question mark), a
// title of every character the sanitizer rewrites, a trailing dot and a
// 390-byte title (task F1) -- and with status.path then written, the file
// path stays what it was. Until 2026-11-10 status.path carried the "?"
// while EpisodeFilePath's whole-path sanitize placed the files under the
// folder without it.
func TestPathAgreesWithTheImport(t *testing.T) {
	root := jellyfinRoot("/data/media/tv")
	seriesTitled := func(title string) *catalogv1alpha1.Series {
		return &catalogv1alpha1.Series{
			ObjectMeta: metav1.ObjectMeta{Name: "what-if", Namespace: "default"},
			Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 266189, SeriesType: catalogv1alpha1.SeriesTypeStandard, RootFolderRef: "tv"},
			Status:     catalogv1alpha1.SeriesStatus{Metadata: &catalogv1alpha1.SeriesMetadata{Title: title, Year: 2021}},
		}
	}
	eps := []catalogv1alpha1.Episode{{
		Spec:   catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: 1},
		Status: catalogv1alpha1.EpisodeStatus{Title: "What If... Captain Carter Were the First Avenger?"},
	}}
	for _, title := range []string{
		"Plain Title",
		"What If...?",
		`A:B\C/D>E<F?G*H|I"J`,
		"Trailing dot.",
		strings.Repeat("Long ", 78),
	} {
		s := seriesTitled(title)
		c, ok := catalogctx.Episode(s, eps)
		require.True(t, ok, title)
		file, err := catalogctx.EpisodeFilePath(root, s, c, ".mkv")
		require.NoError(t, err, title)
		// The reconciler's own folder context: the series' identity alone.
		folderCtx := naming.Context{Kind: commonv1.MediaKindSeries, SeriesTitle: c.SeriesTitle, SeriesYear: c.SeriesYear, TvdbID: c.TvdbID}
		got, err := series.Path(root, s.Spec.Folder, folderCtx)
		require.NoError(t, err, title)
		assert.Equal(t, filepath.Dir(filepath.Dir(file)), got, "a series titled %q: its folder holds the season folder", title)
		assert.LessOrEqual(t, len(filepath.Base(got)), naming.DefaultMaxComponentBytes, "a folder component fits the filesystem: %q", title)

		s.Status.Path = got
		again, err := catalogctx.EpisodeFilePath(root, s, c, ".mkv")
		require.NoError(t, err, title)
		assert.Equal(t, file, again, "the written status.path renders the same file path: %q", title)

		s.Status.Path = ""
		s.Spec.SeasonFolder = new(false)
		file, err = catalogctx.EpisodeFilePath(root, s, c, ".mkv")
		require.NoError(t, err, title)
		assert.Equal(t, filepath.Dir(file), got, "without a season folder the file lies in the series folder itself: %q", title)
	}
	s := seriesTitled("What If...?")
	c, ok := catalogctx.Episode(s, eps)
	require.True(t, ok)
	got, err := series.Path(root, nil, naming.Context{Kind: commonv1.MediaKindSeries, SeriesTitle: c.SeriesTitle, SeriesYear: c.SeriesYear, TvdbID: c.TvdbID})
	require.NoError(t, err)
	assert.Equal(t, "/data/media/tv/What If... (2021) [tvdbid-266189]", got, "the sanitized title, never the title as written")
}
