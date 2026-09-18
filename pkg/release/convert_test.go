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

func TestApplyToCopiesParsedFieldsOntoReleaseInfoWithoutTouchingIndexerFields(t *testing.T) {
	p, err := release.ParseKind("Dune.Part.Two.2024.PROPER.1080p.BluRay.x264-GROUP", commonv1.MediaKindMovie)
	require.NoError(t, err)

	ri := &commonv1.ReleaseInfo{
		GUID:        "keep-me",
		DownloadURL: "https://example/keep-me.torrent",
	}
	p.ApplyTo(ri)

	assert.Equal(t, "keep-me", ri.GUID, "ApplyTo must not touch indexer-sourced fields")
	assert.Equal(t, "https://example/keep-me.torrent", ri.DownloadURL)
	assert.Equal(t, p.Quality, ri.Quality)
	assert.Equal(t, p.Revision, ri.Revision)
	assert.Equal(t, p.Group, ri.ReleaseGroup)
	assert.Equal(t, p.Languages, ri.Languages)
}
