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

package release_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

func TestMatchTitlePrefersExactYearOverTypo(t *testing.T) {
	p, err := release.ParseKind("The.Matrix.1999.1080p.BluRay.x264-GROUP", commonv1.MediaKindMovie)
	require.NoError(t, err)

	cands := []release.TitleCandidate{
		{Title: "The Matrix Reloaded", Year: 2003},
		{Title: "The Matrix", Year: 1999},
		{Title: "Matrix", Year: 1999},
	}
	best, score := release.MatchTitle(p, cands)
	assert.Equal(t, 1, best)
	assert.Greater(t, score, 0.0)
}

func TestMatchTitleWithNoCandidatesReturnsMinusOne(t *testing.T) {
	p, err := release.ParseKind("The.Matrix.1999.1080p.BluRay.x264-GROUP", commonv1.MediaKindMovie)
	require.NoError(t, err)
	best, score := release.MatchTitle(p, nil)
	assert.Equal(t, -1, best)
	assert.Zero(t, score)
}
