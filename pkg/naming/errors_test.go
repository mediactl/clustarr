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

func TestRenderReturnsErrUnknownTokenAndLeavesOutputUnset(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	got, err := e.Render("{Not A Real Token}", naming.Context{})
	require.ErrorIs(t, err, naming.ErrUnknownToken)
	require.Empty(t, got)
}

func TestBuildFolderReturnsErrNoFolderForUnknownKind(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	_, err := e.BuildFolder(commonv1.MediaKind("bogus"), naming.Context{})
	require.ErrorIs(t, err, naming.ErrNoFolder)
}

// TestEpisodeFileWithNoEpisodesDoesNotPanic exercises EpisodeFile end to
// end with a nil Episodes slice. It does not exercise formatEpisodeRange's
// own empty guard directly, since the standard episode-file template never
// emits the internal {episodeRange} token -- see
// TestEpisodeAndAbsoluteRangeTokensAreEmptyForEveryStyleWhenNoEpisodes in
// series_test.go for that.
func TestEpisodeFileWithNoEpisodesDoesNotPanic(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	got, err := e.EpisodeFile(naming.Context{SeriesTitle: "X", Episodes: nil})
	require.NoError(t, err, "no episodes must render an empty episode-range token, not panic")
	require.NotContains(t, got, "<nil>")
}
