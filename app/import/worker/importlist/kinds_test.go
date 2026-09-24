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

package importlist

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

func TestUnyieldableKinds(t *testing.T) {
	arr := func(k catalogv1alpha1.ArrKind) catalogv1alpha1.ImportListSpec {
		return catalogv1alpha1.ImportListSpec{Arr: &catalogv1alpha1.ArrList{BaseURL: "http://x", Kind: k}}
	}
	cases := []struct {
		name  string
		spec  catalogv1alpha1.ImportListSpec
		kinds []string
		want  []string
	}{
		{"trakt video", catalogv1alpha1.ImportListSpec{Trakt: &catalogv1alpha1.TraktList{}}, []string{"movie", "series"}, nil},
		{"trakt album", catalogv1alpha1.ImportListSpec{Trakt: &catalogv1alpha1.TraktList{}}, []string{"movie", "album"}, []string{"album"}},
		{"plex book", catalogv1alpha1.ImportListSpec{Plex: &catalogv1alpha1.PlexWatchlist{}}, []string{"book"}, []string{"book"}},
		{"tmdb audiobook", catalogv1alpha1.ImportListSpec{Tmdb: &catalogv1alpha1.TmdbList{}}, []string{"audiobook"}, []string{"audiobook"}},
		{"mdblist comic", catalogv1alpha1.ImportListSpec{Mdblist: &catalogv1alpha1.MdbList{}}, []string{"series", "comic"}, []string{"comic"}},
		{
			"imdbCSV every non-video kind",
			catalogv1alpha1.ImportListSpec{ImdbCSV: &catalogv1alpha1.CSVList{ConfigMapRef: corev1.LocalObjectReference{Name: "c"}}},
			[]string{"album", "book", "audiobook", "comic"},
			[]string{"album", "book", "audiobook", "comic"},
		},
		{"stevenLu movie", catalogv1alpha1.ImportListSpec{StevenLu: &catalogv1alpha1.StevenLu{}}, []string{"movie"}, nil},
		{"stevenLu series", catalogv1alpha1.ImportListSpec{StevenLu: &catalogv1alpha1.StevenLu{}}, []string{"movie", "series"}, []string{"series"}},
		{
			"custom anything",
			catalogv1alpha1.ImportListSpec{Custom: &catalogv1alpha1.CustomList{}},
			[]string{"movie", "series", "album", "book", "audiobook", "comic"},
			nil,
		},
		{"radarr movie", arr(catalogv1alpha1.ArrKindRadarr), []string{"movie"}, nil},
		{"radarr series", arr(catalogv1alpha1.ArrKindRadarr), []string{"series"}, []string{"series"}},
		{"sonarr series", arr(catalogv1alpha1.ArrKindSonarr), []string{"series"}, nil},
		{"lidarr album", arr(catalogv1alpha1.ArrKindLidarr), []string{"album"}, nil},
		{"lidarr movie", arr(catalogv1alpha1.ArrKindLidarr), []string{"movie", "album"}, []string{"movie"}},
		{"readarr book and audiobook", arr(catalogv1alpha1.ArrKindReadarr), []string{"book", "audiobook"}, nil},
		{"readarr comic", arr(catalogv1alpha1.ArrKindReadarr), []string{"comic"}, []string{"comic"}},
		{
			"clustarr anything", arr(catalogv1alpha1.ArrKindClustarr),
			[]string{"movie", "series", "album", "book", "audiobook", "comic"},
			nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.spec.Kinds = tc.kinds
			assert.Equal(t, tc.want, UnyieldableKinds(tc.spec))
		})
	}
}

func TestStrictlyUnder(t *testing.T) {
	cases := []struct {
		path, root string
		want       bool
	}{
		{"/data/media/movies/A (1999)/a.mkv", "/data/media/movies", true},
		{"/data/media/movies/", "/data/media/movies", false},
		{"/data/media/movies", "/data/media/movies", false},
		{"/data/media/movies/../tv/a.mkv", "/data/media/movies", false},
		{"/data/media/moviesX/a.mkv", "/data/media/movies", false},
		{"/data/media/movies/..a.mkv", "/data/media/movies", true},
		{"relative/a.mkv", "/data/media/movies", false},
		{"/data/media/movies/a.mkv", "", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, strictlyUnder(tc.path, tc.root), "%q under %q", tc.path, tc.root)
	}
}

func TestPruneEmptyDirsStopsAtANonEmptyDirAndNeverTakesTheRoot(t *testing.T) {
	root := t.TempDir()
	keep := filepath.Join(root, "Series", "Season 01")
	gone := filepath.Join(root, "Series", "Season 02")
	require.NoError(t, os.MkdirAll(keep, 0o755))
	require.NoError(t, os.MkdirAll(gone, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(keep, "x.nfo"), nil, 0o600))

	pruneEmptyDirs(root, gone)
	assert.NoDirExists(t, gone)
	assert.DirExists(t, keep, "Season 01 still holds a file")

	require.NoError(t, os.Remove(filepath.Join(keep, "x.nfo")))
	pruneEmptyDirs(root, keep)
	assert.NoDirExists(t, filepath.Join(root, "Series"))
	assert.DirExists(t, root, "the root folder itself is never removed")
}
