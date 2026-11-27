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

package release

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// A read that times out is tried again: the limit is wall clock, and a
// process held at its CPU limit spends it without matching anything. On
// talos the manager, at its 750m, read titles it reads every day as
// timeouts while it judged a season's answers (2026-11-27). A title that
// times out every time keeps its error, and any other error is not retried.
func TestParseReadsATitleAgainWhenItsReadTimesOut(t *testing.T) {
	const title = "[Judas] Tensei Kizoku, Kantei Skill de Nariagaru - S03E02 [1080p][HEVC x265 10bit][Multi-Subs] (Weekly)"
	timeout := errors.New("match timeout after 50ms on input `" + title + "`")
	require.True(t, IsTimeout(timeout), "the error is shaped as regexp2's")

	swap := func(fails int, err error) (calls *int) {
		n := 0
		saved := parseAttempt
		t.Cleanup(func() { parseAttempt = saved })
		parseAttempt = func(title string, o Options) (*ParsedRelease, error) {
			n++
			if n <= fails {
				return nil, err
			}
			return saved(title, o)
		}
		return &n
	}

	t.Run("a pause is outlasted", func(t *testing.T) {
		calls := swap(2, timeout)
		p, err := Parse(title, Options{Kind: commonv1.MediaKindEpisode})
		require.NoError(t, err)
		assert.Equal(t, []int{3}, p.Seasons)
		assert.Equal(t, []int{2}, p.Episodes)
		assert.Equal(t, 3, *calls)
	})
	t.Run("a pattern that backtracks keeps its error", func(t *testing.T) {
		calls := swap(100, timeout)
		_, err := Parse(title, Options{Kind: commonv1.MediaKindEpisode})
		require.Error(t, err)
		assert.True(t, IsTimeout(err))
		assert.Equal(t, timeoutTries, *calls)
	})
	t.Run("a title no pattern reads is read once", func(t *testing.T) {
		calls := swap(100, errors.New("release: no pattern matched"))
		_, err := Parse(title, Options{Kind: commonv1.MediaKindEpisode})
		require.Error(t, err)
		assert.Equal(t, 1, *calls)
	})
}
