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

package rollup_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/rollup"
	"github.com/mediactl/clustarr/pkg/quality"
)

func TestFileState(t *testing.T) {
	q := commonv1.Quality{Name: "Bluray-1080p", Resolution: 1080}
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "inception-abc1234567"},
		Spec:       catalogv1alpha1.MediaFileSpec{Quality: q, FormatScore: 40},
	}

	t.Run("no file", func(t *testing.T) {
		hasFile, ref, fq, score, cutoffMet := rollup.FileState(nil, nil)
		assert.False(t, hasFile)
		assert.Nil(t, ref)
		assert.Nil(t, fq)
		assert.Zero(t, score)
		assert.False(t, cutoffMet)
	})

	t.Run("file present, no profile resolved yet", func(t *testing.T) {
		hasFile, ref, fq, score, cutoffMet := rollup.FileState(mf, nil)
		assert.True(t, hasFile)
		require.NotNil(t, ref)
		assert.Equal(t, "inception-abc1234567", *ref)
		require.NotNil(t, fq)
		assert.Equal(t, q, *fq)
		assert.EqualValues(t, 40, score)
		assert.False(t, cutoffMet, "no profile means conservatively not met, never a guessed true")
	})

	t.Run("file present, profile resolved, meets cutoff", func(t *testing.T) {
		p := quality.Profile{Tiers: [][]quality.Definition{{{Quality: q}}}, CutoffIndex: 0}
		_, _, _, _, cutoffMet := rollup.FileState(mf, &p)
		assert.True(t, cutoffMet)
	})

	t.Run("file present, profile resolved, below cutoff", func(t *testing.T) {
		better := commonv1.Quality{Name: "Bluray-2160p", Resolution: 2160}
		p := quality.Profile{Tiers: [][]quality.Definition{{{Quality: better}}, {{Quality: q}}}, CutoffIndex: 0}
		_, _, _, _, cutoffMet := rollup.FileState(mf, &p)
		assert.False(t, cutoffMet)
	})

	// A transcoded file is final, so it meets the cutoff whatever the
	// profile says about the release quality frozen on it at import --
	// the answer that keeps it out of CutoffUnmet and the search rotation.
	t.Run("a transcoded file below the cutoff meets it", func(t *testing.T) {
		better := commonv1.Quality{Name: "Bluray-2160p", Resolution: 2160}
		p := quality.Profile{Tiers: [][]quality.Definition{{{Quality: better}}, {{Quality: q}}}, CutoffIndex: 0}
		transcoded := mf.DeepCopy()
		transcoded.Spec.Original = ptr.To(false)
		hasFile, _, fq, _, cutoffMet := rollup.FileState(transcoded, &p)
		assert.True(t, hasFile)
		require.NotNil(t, fq)
		assert.Equal(t, q, *fq, "the frozen release quality is still reported as is")
		assert.True(t, cutoffMet)
	})

	t.Run("a transcoded file meets the cutoff with no profile resolved", func(t *testing.T) {
		tagged := mf.DeepCopy()
		tagged.Status.MediaInfo = &commonv1.MediaInfo{TranscodeProfile: "default@abc"}
		_, _, _, _, cutoffMet := rollup.FileState(tagged, nil)
		assert.True(t, cutoffMet)
	})
}
