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

package decision

import (
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

func TestTargetRuntimeMinutes(t *testing.T) {
	t.Run("movie with known runtime", func(t *testing.T) {
		m, ok := targetRuntimeMinutes(Target{Kind: common.MediaKindMovie, RuntimeMinutes: 95}, &release.ParsedRelease{})
		require.True(t, ok)
		require.Equal(t, 95, m)
	})

	t.Run("movie with unknown runtime falls back to 110", func(t *testing.T) {
		m, ok := targetRuntimeMinutes(Target{Kind: common.MediaKindMovie, RuntimeMinutes: 0}, &release.ParsedRelease{})
		require.True(t, ok)
		require.Equal(t, 110, m)
	})

	t.Run("single episode with known runtime", func(t *testing.T) {
		tg := Target{Kind: common.MediaKindEpisode, EpisodeRuntimes: []int{42}}
		m, ok := targetRuntimeMinutes(tg, &release.ParsedRelease{Episodes: []int{5}})
		require.True(t, ok)
		require.Equal(t, 42, m)
	})

	t.Run("single episode with unknown runtime falls back to 45", func(t *testing.T) {
		tg := Target{Kind: common.MediaKindEpisode, EpisodeRuntimes: []int{0}}
		m, ok := targetRuntimeMinutes(tg, &release.ParsedRelease{Episodes: []int{5}})
		require.True(t, ok)
		require.Equal(t, 45, m)
	})

	t.Run("season pack sums per-episode runtimes, falling back per episode", func(t *testing.T) {
		tg := Target{Kind: common.MediaKindEpisode, EpisodeRuntimes: []int{42, 42, 0, 42}}
		m, ok := targetRuntimeMinutes(tg, &release.ParsedRelease{Episodes: []int{1, 2, 3, 4}})
		require.True(t, ok)
		require.Equal(t, 171, m) // 42+42+45+42, the third episode's unknown runtime falls back to 45
	})

	t.Run("full-season release with no per-episode runtime data falls back to one 45-minute episode", func(t *testing.T) {
		tg := Target{Kind: common.MediaKindEpisode}
		m, ok := targetRuntimeMinutes(tg, &release.ParsedRelease{FullSeason: true})
		require.True(t, ok)
		require.Equal(t, 45, m)
	})

	t.Run("non-video kind has no runtime/size model", func(t *testing.T) {
		_, ok := targetRuntimeMinutes(Target{Kind: common.MediaKindBook}, &release.ParsedRelease{})
		require.False(t, ok)
	})
}
