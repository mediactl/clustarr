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

package episode_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/episode"
)

// TestFileState only proves episode.FileState re-exports rollup.FileState
// correctly -- the full decision table is table-tested once, in
// catalogarr/controller/rollup, per the C6 controller amendment.
func TestFileState(t *testing.T) {
	q := commonv1.Quality{Name: "Bluray-1080p", Resolution: 1080}
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "expanse-s01e01-abc1234567"},
		Spec:       catalogv1alpha1.MediaFileSpec{Quality: q, FormatScore: 25},
	}

	hasFile, ref, fq, score, cutoffMet := episode.FileState(mf, nil)
	assert.True(t, hasFile)
	require.NotNil(t, ref)
	assert.Equal(t, "expanse-s01e01-abc1234567", *ref)
	require.NotNil(t, fq)
	assert.Equal(t, q, *fq)
	assert.EqualValues(t, 25, score)
	assert.False(t, cutoffMet)

	hasFile, ref, fq, score, cutoffMet = episode.FileState(nil, nil)
	assert.False(t, hasFile)
	assert.Nil(t, ref)
	assert.Nil(t, fq)
	assert.Zero(t, score)
	assert.False(t, cutoffMet)
}
