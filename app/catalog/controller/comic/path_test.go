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

package comic_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/comic"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
)

func comicRoot(path string) *catalogv1alpha1.RootFolder {
	return &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "comics", Namespace: "default"},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: path, Kind: catalogv1alpha1.RootFolderKindComic,
			Naming: catalogv1alpha1.NamingSpec{Dialect: catalogv1alpha1.NamingDialectJellyfin},
		},
	}
}

// TestPath's expected folder is pkg/naming/preset.go's BuildFolder default
// for MediaKindComic, TokenComicFolder's "{Comic Series Title}" template --
// Comic has no dedicated *Folder Engine method the way Series does
// (comic.Path goes through catalogctx.ItemFolder's BuildFolder; see
// comic/path.go's doc comment), and that template renders ComicSeriesTitle
// verbatim regardless of dialect.
func TestPath(t *testing.T) {
	root := comicRoot("/data/media/comics")
	ctx := naming.Context{Kind: commonv1.MediaKindComic, ComicSeriesTitle: "The Amazing Comic"}

	got, err := comic.Path(root, nil, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/comics/The Amazing Comic", got)

	override := "The Amazing Comic (Deluxe)"
	got, err = comic.Path(root, &override, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/comics/The Amazing Comic (Deluxe)", got)

	got, err = comic.Path(root, nil, naming.Context{Kind: commonv1.MediaKindComic})
	require.NoError(t, err)
	assert.Equal(t, "/data/media/comics", got, "a comic without a title yet lives at the root")

	_, err = comic.Path(root, nil, naming.Context{Kind: commonv1.MediaKindComic, ComicSeriesTitle: "..."})
	require.ErrorIs(t, err, naming.ErrUnsafeComponent, "a title of only dots is an error, never a path")
	_, err = comic.Path(root, nil, naming.Context{Kind: commonv1.MediaKindComic, ComicSeriesTitle: "???"})
	require.Error(t, err, "a title that sanitizes to nothing is an error, never a path")
}

// TestPathAgreesWithTheImport: the folder the controller writes is the one
// the import places the comic's issue files in (catalogctx.IssueFilePath),
// byte for byte, both rendered from one Comic through the import's own
// context -- over a plain title, Marvel's "What If...?" (a trailing dot
// behind a question mark), a title of every character the sanitizer
// rewrites, a trailing dot and a 390-byte title (task F1) -- and with
// status.path then written, the file path stays what it was. Until
// 2026-11-10 status.path carried the title as written while
// IssueFilePath's whole-path sanitize placed the files under the folder
// without the "?".
func TestPathAgreesWithTheImport(t *testing.T) {
	root := comicRoot("/data/media/comics")
	issue := &catalogv1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{Name: "what-if-001.0", Namespace: "default"},
		Spec:       catalogv1alpha1.IssueSpec{ComicRef: "what-if", Number: "1"},
	}
	for _, title := range []string{
		"Plain Title",
		"What If...?",
		`A:B\C/D>E<F?G*H|I"J`,
		"Trailing dot.",
		strings.Repeat("Long ", 78),
	} {
		cm := &catalogv1alpha1.Comic{
			ObjectMeta: metav1.ObjectMeta{Name: "what-if", Namespace: "default"},
			Spec:       catalogv1alpha1.ComicSpec{RootFolderRef: "comics", SpecialVersion: catalogv1alpha1.ComicSpecialVersionTPB},
			Status:     catalogv1alpha1.ComicStatus{Metadata: &catalogv1alpha1.ComicMetadata{Title: title, Year: 2015}},
		}
		c, ok := catalogctx.Issue(cm, issue)
		require.True(t, ok, title)
		file, err := catalogctx.IssueFilePath(root, cm, c, ".cbz")
		require.NoError(t, err, title)
		// The reconciler's own folder context: what catalogctx.Issue gives
		// the file context, less the issue's number.
		folderCtx := naming.Context{
			Kind: commonv1.MediaKindComic, ComicSeriesTitle: cm.Status.Metadata.Title, Year: int(cm.Status.Metadata.Year),
			SpecialVersion: catalogctx.SpecialVersionLabel(cm.Spec.SpecialVersion),
		}
		got, err := comic.Path(root, cm.Spec.Folder, folderCtx)
		require.NoError(t, err, title)
		assert.Equal(t, filepath.Dir(file), got, "a comic titled %q", title)
		assert.LessOrEqual(t, len(filepath.Base(got)), naming.DefaultMaxComponentBytes, "a folder component fits the filesystem: %q", title)

		cm.Status.Path = got
		again, err := catalogctx.IssueFilePath(root, cm, c, ".cbz")
		require.NoError(t, err, title)
		assert.Equal(t, file, again, "the written status.path renders the same file path: %q", title)
	}
	got, err := comic.Path(root, nil, naming.Context{Kind: commonv1.MediaKindComic, ComicSeriesTitle: "What If...?"})
	require.NoError(t, err)
	assert.Equal(t, "/data/media/comics/What If", got, "the sanitized title, never the title as written")
}
