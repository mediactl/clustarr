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

package audiobook_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/audiobook"
	"github.com/mediactl/clustarr/pkg/quality"
)

func mfPart(name, path string) catalogv1alpha1.MediaFile {
	return catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: catalogv1alpha1.MediaFileSpec{
			Path:    path,
			Quality: commonv1.Quality{Name: "MP3"},
		},
	}
}

func TestFileState(t *testing.T) {
	t.Run("no files", func(t *testing.T) {
		hasFile, refs, q, cutoffMet := audiobook.FileState(nil, nil)
		assert.False(t, hasFile)
		assert.Nil(t, refs)
		assert.Nil(t, q)
		assert.False(t, cutoffMet)
	})

	t.Run("multiple parts are ordered by path, not creation or list order", func(t *testing.T) {
		items := []catalogv1alpha1.MediaFile{
			mfPart("book-part03", "/data/media/audiobooks/Book/Book 03.mp3"),
			mfPart("book-part01", "/data/media/audiobooks/Book/Book 01.mp3"),
			mfPart("book-part02", "/data/media/audiobooks/Book/Book 02.mp3"),
		}
		hasFile, refs, q, _ := audiobook.FileState(items, nil)
		assert.True(t, hasFile)
		require.NotNil(t, q)
		assert.Equal(t, []string{"book-part01", "book-part02", "book-part03"}, refs)
	})

	t.Run("no profile resolved yet: quality is still reported, cutoffMet conservatively false", func(t *testing.T) {
		items := []catalogv1alpha1.MediaFile{mfPart("solo", "/data/media/audiobooks/Book/Book.mp3")}
		hasFile, refs, q, cutoffMet := audiobook.FileState(items, nil)
		assert.True(t, hasFile)
		assert.Equal(t, []string{"solo"}, refs)
		require.NotNil(t, q)
		assert.Equal(t, "MP3", q.Name)
		assert.False(t, cutoffMet, "no profile means conservatively not met, never a guessed true")
	})

	t.Run("profile resolved, meets cutoff", func(t *testing.T) {
		items := []catalogv1alpha1.MediaFile{mfPart("solo", "/data/media/audiobooks/Book/Book.mp3")}
		p := quality.Profile{Tiers: [][]quality.Definition{{{Quality: commonv1.Quality{Name: "MP3"}}}}, CutoffIndex: 0}
		_, _, _, cutoffMet := audiobook.FileState(items, &p)
		assert.True(t, cutoffMet)
	})

	t.Run("profile resolved, below cutoff", func(t *testing.T) {
		items := []catalogv1alpha1.MediaFile{mfPart("solo", "/data/media/audiobooks/Book/Book.mp3")}
		better := commonv1.Quality{Name: "FLAC"}
		p := quality.Profile{Tiers: [][]quality.Definition{{{Quality: better}}, {{Quality: commonv1.Quality{Name: "MP3"}}}}, CutoffIndex: 0}
		_, _, _, cutoffMet := audiobook.FileState(items, &p)
		assert.False(t, cutoffMet)
	})

	t.Run("caps fileRefs at 200", func(t *testing.T) {
		items := make([]catalogv1alpha1.MediaFile, 0, 250)
		for i := 0; i < 250; i++ {
			items = append(items, mfPart(fmt.Sprintf("part-%03d", i), fmt.Sprintf("/data/media/audiobooks/Book/%03d.mp3", i)))
		}
		hasFile, refs, _, _ := audiobook.FileState(items, nil)
		assert.True(t, hasFile)
		assert.Len(t, refs, 200)
		assert.Equal(t, "part-000", refs[0])
		assert.Equal(t, "part-199", refs[199])
	})
}
