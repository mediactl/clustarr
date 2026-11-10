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
			Path:        path,
			Quality:     commonv1.Quality{Name: "MP3"},
			FormatScore: 3,
		},
	}
}

func mfIndexedPart(name, path string, index int32) catalogv1alpha1.MediaFile {
	mf := mfPart(name, path)
	mf.Spec.PartIndex = index
	return mf
}

func TestFileState(t *testing.T) {
	t.Run("no files", func(t *testing.T) {
		hasFile, refs, partCount, q, score, cutoffMet := audiobook.FileState(nil, nil)
		assert.False(t, hasFile)
		assert.Nil(t, refs)
		assert.Zero(t, partCount)
		assert.Nil(t, q)
		assert.Zero(t, score)
		assert.False(t, cutoffMet)
	})

	t.Run("parts are ordered by spec.partIndex, the import's play order (design D11)", func(t *testing.T) {
		items := []catalogv1alpha1.MediaFile{
			mfIndexedPart("book-part03", "/data/media/audiobooks/Book/Book - Chapter Three.mp3", 3),
			mfIndexedPart("book-part01", "/data/media/audiobooks/Book/Book - Chapter One.mp3", 1),
			mfIndexedPart("book-part02", "/data/media/audiobooks/Book/Book - Chapter Two.mp3", 2),
		}
		hasFile, refs, partCount, q, _, _ := audiobook.FileState(items, nil)
		assert.True(t, hasFile)
		require.NotNil(t, q)
		assert.Equal(t, []string{"book-part01", "book-part02", "book-part03"}, refs)
		assert.Equal(t, int32(3), partCount)
	})

	t.Run("parts without an index are ordered by path, after the indexed ones", func(t *testing.T) {
		items := []catalogv1alpha1.MediaFile{
			mfPart("book-part03", "/data/media/audiobooks/Book/Book 03.mp3"),
			mfPart("book-part01", "/data/media/audiobooks/Book/Book 01.mp3"),
			mfIndexedPart("book-intro", "/data/media/audiobooks/Book/Book 99 intro.mp3", 1),
			mfPart("book-part02", "/data/media/audiobooks/Book/Book 02.mp3"),
		}
		_, refs, partCount, _, _, _ := audiobook.FileState(items, nil)
		assert.Equal(t, []string{"book-intro", "book-part01", "book-part02", "book-part03"}, refs)
		assert.Equal(t, int32(4), partCount)
	})

	t.Run("no profile resolved yet: the frozen quality and score are still reported, cutoffMet conservatively false", func(t *testing.T) {
		items := []catalogv1alpha1.MediaFile{mfPart("solo", "/data/media/audiobooks/Book/Book.mp3")}
		hasFile, refs, partCount, q, score, cutoffMet := audiobook.FileState(items, nil)
		assert.True(t, hasFile)
		assert.Equal(t, []string{"solo"}, refs)
		assert.Equal(t, int32(1), partCount)
		require.NotNil(t, q)
		assert.Equal(t, "MP3", q.Name)
		assert.Equal(t, int32(3), score, "the representative part's frozen format score (design D5)")
		assert.False(t, cutoffMet, "no profile means conservatively not met, never a guessed true")
	})

	t.Run("profile resolved, meets cutoff", func(t *testing.T) {
		items := []catalogv1alpha1.MediaFile{mfPart("solo", "/data/media/audiobooks/Book/Book.mp3")}
		p := quality.Profile{Tiers: [][]quality.Definition{{{Quality: commonv1.Quality{Name: "MP3"}}}}, CutoffIndex: 0}
		_, _, _, _, _, cutoffMet := audiobook.FileState(items, &p)
		assert.True(t, cutoffMet)
	})

	t.Run("profile resolved, below cutoff", func(t *testing.T) {
		items := []catalogv1alpha1.MediaFile{mfPart("solo", "/data/media/audiobooks/Book/Book.mp3")}
		better := commonv1.Quality{Name: "FLAC"}
		p := quality.Profile{Tiers: [][]quality.Definition{{{Quality: better}}, {{Quality: commonv1.Quality{Name: "MP3"}}}}, CutoffIndex: 0}
		_, _, _, _, _, cutoffMet := audiobook.FileState(items, &p)
		assert.False(t, cutoffMet)
	})

	t.Run("caps fileRefs at 200 and still counts every part on disk", func(t *testing.T) {
		items := make([]catalogv1alpha1.MediaFile, 0, 250)
		for i := 0; i < 250; i++ {
			items = append(items, mfIndexedPart(fmt.Sprintf("part-%03d", i), fmt.Sprintf("/data/media/audiobooks/Book/%03d.mp3", i), int32(i+1)))
		}
		hasFile, refs, partCount, _, _, _ := audiobook.FileState(items, nil)
		assert.True(t, hasFile)
		assert.Len(t, refs, 200)
		assert.Equal(t, "part-000", refs[0])
		assert.Equal(t, "part-199", refs[199])
		assert.Equal(t, int32(250), partCount, "partCount is the parts on disk, not the capped list")
	})
}
