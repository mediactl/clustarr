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

package catalogctx

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
)

// A release title's format names are worked out once: the file pass renders
// every file's name on every pass, and parsing the title and running the
// catalogue's regexes again was 7% of the manager's CPU (talos, 2026-11-03).
func TestFormatNamesAreRememberedByTitle(t *testing.T) {
	ctx := context.Background()
	c := naming.Context{Kind: commonv1.MediaKindMovie, ReleaseTitle: "Heat.1995.REPACK2.1080p.AMZN.WEB-DL.DDP5.1.H.264-GROUP"}
	formatNamesMemo.Lock()
	formatNamesMemo.m = map[formatNamesKey][]string{}
	formatNamesMemo.Unlock()

	first := formatNames(ctx, c, nil)
	assert.Equal(t, []string{"AMZN", "Repack2"}, first, "a REPACK2 AMZN title names both")
	again := formatNames(ctx, c, nil)
	assert.Equal(t, first, again)
	assert.LessOrEqual(t, testing.AllocsPerRun(20, func() { formatNames(ctx, c, nil) }), 2.0,
		"a remembered title still parses")

	// Each input is its own answer: the anime guide, the frozen formats.
	anime := c
	anime.Anime = true
	assert.Equal(t, formatNames(ctx, anime, nil), formatNames(ctx, anime, nil))
	frozen := formatNames(ctx, c, []string{"amzn"})
	assert.Equal(t, first, frozen, "a frozen format the title names is named once")
	assert.Equal(t, first, formatNames(ctx, c, nil), "the memo answers each input by its own key")
	first[0] = "changed"
	assert.Equal(t, []string{"AMZN", "Repack2"}, formatNames(ctx, c, nil), "a caller's edit never reaches the memo")
}
