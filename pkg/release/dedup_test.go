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

func TestFingerprintIsStableAndDistinguishesQuality(t *testing.T) {
	a, err := release.ParseKind("Dune.Part.Two.2024.1080p.BluRay.x264-GROUP", commonv1.MediaKindMovie)
	require.NoError(t, err)
	b, err := release.ParseKind("Dune.Part.Two.2024.1080p.Bluray.x264-GROUP", commonv1.MediaKindMovie) // different case, same release
	require.NoError(t, err)
	c, err := release.ParseKind("Dune.Part.Two.2024.2160p.BluRay.x264-GROUP", commonv1.MediaKindMovie) // different resolution
	require.NoError(t, err)

	assert.Equal(t, a.Fingerprint(), b.Fingerprint(), "case-only differences must collapse")
	assert.NotEqual(t, a.Fingerprint(), c.Fingerprint(), "different quality must not collide")
	assert.Len(t, a.Fingerprint(), 16)
}

func TestFingerprintDistinguishesNonLatinTitles(t *testing.T) {
	a, err := release.ParseKind("Матрица.1999.1080p.BluRay.x264-GROUP", commonv1.MediaKindMovie)
	require.NoError(t, err)
	b, err := release.ParseKind("Дюна.1999.1080p.BluRay.x264-GROUP", commonv1.MediaKindMovie)
	require.NoError(t, err)
	assert.NotEqual(t, a.Fingerprint(), b.Fingerprint(), "two different non-Latin titles must not collide")
}
