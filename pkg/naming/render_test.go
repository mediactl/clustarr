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

package naming_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
)

func TestRenderSubstitutesSimpleTokens(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	c := naming.Context{Kind: commonv1.MediaKindMovie, Title: "The Matrix", Year: 1999}

	got, err := e.Render("{Movie Title} ({Release Year})", c)

	require.NoError(t, err)
	require.Equal(t, "The Matrix (1999)", got)
}

func TestRenderLeavesLiteralTextAlone(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	got, err := e.Render("no tokens here", naming.Context{})
	require.NoError(t, err)
	require.Equal(t, "no tokens here", got)
}

func TestRenderOmitsWrapperWhenTokenIsEmpty(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	got, err := e.Render("{Movie Title}{-Release Group}", naming.Context{Title: "Heat"})
	require.NoError(t, err)
	require.Equal(t, "Heat", got, "empty Release Group must drop the leading dash too")
}

func TestRenderKeepsWrapperWhenTokenIsPresent(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	got, err := e.Render("{Movie Title}{-Release Group}", naming.Context{Title: "Heat", ReleaseGroup: "RlsGrp"})
	require.NoError(t, err)
	require.Equal(t, "Heat-RlsGrp", got)
}

func TestRenderProviderIDTokens(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	c := naming.Context{Title: "The Matrix", Year: 1999, TmdbID: "603"}
	got, err := e.Render("{Movie Title} ({Release Year}) [tmdbid-{TmdbId}]", c)
	require.NoError(t, err)
	require.Equal(t, "The Matrix (1999) [tmdbid-603]", got)
}

func TestRenderSplitBracketAcrossTwoAdjacentTokens(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	c := naming.Context{MediaInfo: commonv1.MediaInfo{Audio: []commonv1.AudioStream{{Codec: "EAC3", Channels: 6}}}}
	got, err := e.Render("{[MediaInfo AudioCodec}{ MediaInfo AudioChannels]}", c)
	require.NoError(t, err)
	require.Equal(t, "[EAC3 5.1]", got, "note A2's split-bracket idiom: two single-brace tokens forming one pair")
}
