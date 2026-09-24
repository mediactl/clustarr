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

package artist_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/artist"
	"github.com/mediactl/clustarr/pkg/naming"
)

func TestPathUsesFolderOverrideWhenSet(t *testing.T) {
	override := "Radiohead (custom)"
	got, err := artist.Path("/music", &override, naming.NewEngine(naming.Config{}), naming.Context{})
	require.NoError(t, err)
	assert.Equal(t, "/music/Radiohead (custom)", got)
}

func TestPathFallsBackToTheNamingEngine(t *testing.T) {
	eng := naming.NewEngine(naming.Config{Dialect: naming.DialectPlex})
	ctx := naming.Context{Kind: commonv1.MediaKindArtist, ArtistName: "Radiohead"}
	got, err := artist.Path("/music", nil, eng, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/music/Radiohead", got)
}

func TestPathEmptyFolderOverrideFallsBackToo(t *testing.T) {
	empty := ""
	eng := naming.NewEngine(naming.Config{Dialect: naming.DialectPlex})
	ctx := naming.Context{Kind: commonv1.MediaKindArtist, ArtistName: "Radiohead"}
	got, err := artist.Path("/music", &empty, eng, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/music/Radiohead", got, "an explicitly empty override is treated as unset, mirroring series.Path")
}
