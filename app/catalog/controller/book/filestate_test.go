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

package book_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/book"
	"github.com/mediactl/clustarr/pkg/quality"
)

// ebookProfile mirrors pkg/quality/catalogue/data/profiles/ebook.json's real
// tier order (PDF < MOBI < EPUB < AZW3, cutoff MOBI) without needing the
// full CRD/catalogue machinery, the same shape
// app/catalog/controller/rollup/filestate_test.go builds its test profiles
// with.
func ebookProfile() quality.Profile {
	return quality.Profile{
		Tiers: [][]quality.Definition{
			{{Quality: commonv1.Quality{Name: "AZW3"}}},
			{{Quality: commonv1.Quality{Name: "EPUB"}}},
			{{Quality: commonv1.Quality{Name: "MOBI"}}}, // cutoff
			{{Quality: commonv1.Quality{Name: "PDF"}}},
		},
		CutoffIndex: 2,
	}
}

func TestFileState(t *testing.T) {
	t.Run("nil MediaFile is the zero value", func(t *testing.T) {
		hasFile, fileRef, fileQuality, fileFormatScore, fileFormat, cutoffMet := book.FileState(nil, nil)
		assert.False(t, hasFile)
		assert.Nil(t, fileRef)
		assert.Nil(t, fileQuality)
		assert.Zero(t, fileFormatScore)
		assert.Empty(t, fileFormat)
		assert.False(t, cutoffMet)
	})

	t.Run("file present, no profile resolved yet: the frozen quality is reported, cutoff conservatively false", func(t *testing.T) {
		mf := &catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: "the-hobbit-mf"},
			Spec: catalogv1alpha1.MediaFileSpec{
				Path:        "/data/books/Tolkien/The Hobbit.epub",
				Quality:     commonv1.Quality{Name: "EPUB"},
				FormatScore: 7,
			},
		}
		hasFile, fileRef, fileQuality, fileFormatScore, fileFormat, cutoffMet := book.FileState(mf, nil)
		assert.True(t, hasFile)
		require.NotNil(t, fileRef)
		assert.Equal(t, "the-hobbit-mf", *fileRef)
		require.NotNil(t, fileQuality)
		assert.Equal(t, "EPUB", fileQuality.Name, "the quality is the file's frozen spec.quality (design D5)")
		assert.Equal(t, int32(7), fileFormatScore, "the score is the file's frozen spec.formatScore")
		assert.Equal(t, "EPUB", fileFormat, "the format is the quality's name as the book ladder names it")
		assert.False(t, cutoffMet, "no profile means conservatively not met, never a guessed true")
	})

	t.Run("a file whose quality the import left unset has no format: the path's extension is no longer read", func(t *testing.T) {
		mf := &catalogv1alpha1.MediaFile{Spec: catalogv1alpha1.MediaFileSpec{Path: "/data/books/Tolkien/lotr.AZW3"}}
		hasFile, _, fileQuality, _, fileFormat, _ := book.FileState(mf, nil)
		assert.True(t, hasFile)
		require.NotNil(t, fileQuality)
		assert.Empty(t, fileQuality.Name)
		assert.Empty(t, fileFormat, "the import freezes the quality; nothing is invented from the name")
	})

	t.Run("a file at or better than the profile's cutoff meets it", func(t *testing.T) {
		p := ebookProfile()
		mf := &catalogv1alpha1.MediaFile{Spec: catalogv1alpha1.MediaFileSpec{Quality: commonv1.Quality{Name: "MOBI"}}}
		_, _, _, _, fileFormat, cutoffMet := book.FileState(mf, &p)
		assert.Equal(t, "MOBI", fileFormat)
		assert.True(t, cutoffMet)
		mfBetter := &catalogv1alpha1.MediaFile{Spec: catalogv1alpha1.MediaFileSpec{Quality: commonv1.Quality{Name: "AZW3"}}}
		_, _, _, _, _, cutoffMetBetter := book.FileState(mfBetter, &p)
		assert.True(t, cutoffMetBetter)
	})

	t.Run("a file below the profile's cutoff does not meet it", func(t *testing.T) {
		p := ebookProfile()
		mf := &catalogv1alpha1.MediaFile{Spec: catalogv1alpha1.MediaFileSpec{Quality: commonv1.Quality{Name: "PDF"}}}
		_, _, fileQuality, _, fileFormat, cutoffMet := book.FileState(mf, &p)
		assert.Equal(t, "PDF", fileFormat)
		assert.Equal(t, "PDF", fileQuality.Name)
		assert.False(t, cutoffMet)
	})
}
