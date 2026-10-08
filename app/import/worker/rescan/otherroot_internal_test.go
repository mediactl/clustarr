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
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// TestAFileOfAnotherRootFoldersMovieIsUnmatched is smoke finding O4: a
// library scan attached a file to the Movie carrying its TMDB id -- or its
// title and year -- even when that Movie is stored under another root
// folder, so an ordinary scan made a MediaFile outside its item's root
// folder (the case the import planner refuses to recycle). A scan
// attributes a file only to an item of the root folder it scans; a file
// whose id or title names another root folder's Movie is listed unmatched,
// naming that Movie and its root folder, and nothing is recorded or created.
func TestAFileOfAnotherRootFoldersMovieIsUnmatched(t *testing.T) {
	for name, folder := range map[string]string{
		"by its TMDB id":        "Inception (2010) {tmdb-27205}",
		"by its title and year": "Inception (2010)",
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			hdDir, uhdDir := t.TempDir(), t.TempDir()
			root := func(name, path string) *catalogv1alpha1.RootFolder {
				return &catalogv1alpha1.RootFolder{
					ObjectMeta: metav1.ObjectMeta{Namespace: "films", Name: name},
					Spec: catalogv1alpha1.RootFolderSpec{
						Path: path, Kind: catalogv1alpha1.RootFolderKindMovie,
						Defaults: catalogv1alpha1.RootDefaults{QualityProfileRef: "hd"},
					},
				}
			}
			hd, uhd := root("movies", hdDir), root("movies-4k", uhdDir)
			inception := &catalogv1alpha1.Movie{
				ObjectMeta: metav1.ObjectMeta{Namespace: "films", Name: "inception-27205"},
				Spec:       catalogv1alpha1.MovieSpec{TmdbID: 27205, QualityProfileRef: "hd", RootFolderRef: hd.Name},
				Status: catalogv1alpha1.MovieStatus{Metadata: &catalogv1alpha1.MovieMetadata{
					Title: "Inception", Year: 2010,
				}},
			}
			w, c, _ := applyingWorker(t, ctx, nil, hd, uhd, inception)

			path := filepath.Join(uhdDir, folder, "Inception (2010).mkv")
			info := plant(t, path)
			st := &scanState{
				scan: &catalogv1alpha1.LibraryScan{ObjectMeta: metav1.ObjectMeta{Namespace: "films", Name: "scan", UID: "scan-uid"}},
				root: uhd,
			}
			require.NoError(t, w.loadMovies(ctx, &st.movies, "films", uhd.Name))
			require.NoError(t, w.attributeMediaFile(ctx, st, path, info, w.probeFor(path, nil)))

			var files catalogv1alpha1.MediaFileList
			require.NoError(t, c.List(ctx, &files))
			assert.Empty(t, files.Items, "a file under movies-4k was recorded against a Movie of movies")
			var movies catalogv1alpha1.MovieList
			require.NoError(t, c.List(ctx, &movies))
			assert.Len(t, movies.Items, 1, "nor is a second Movie created for the same id")
			require.Len(t, st.progress.Unmatched, 1, "the walk lists the file unmatched")
			u := st.progress.Unmatched[0]
			assert.Equal(t, filepath.Join(folder, "Inception (2010).mkv"), u.Path)
			assert.Contains(t, u.Reason, `root folder "movies"`)
			assert.Equal(t, []string{"inception-27205"}, u.Candidates)
		})
	}
}
