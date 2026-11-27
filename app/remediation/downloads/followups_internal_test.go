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

package downloads

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// The episodes a pack did not hold are searched again only where a search
// is wanted: one that holds a file it is content with, or is not monitored,
// is granted none, and a plan that frees nothing owes no effect.
func TestOnlyTheWantedEpisodesAPackDidNotHoldAreSearchedAgain(t *testing.T) {
	now := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	series := schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: "media", Name: "show", UID: "u-show"}}
	ep := func(n string) schema.ItemRef {
		return schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Namespace: "media", Name: "show-" + n, UID: "u-" + n}}
	}
	v := lifecycle.View{
		Now:         now,
		Satisfied:   map[schema.ItemRef]bool{ep("s01e15"): true},
		Unmonitored: map[schema.ItemRef]bool{ep("s01e16"): true},
	}
	assert.Empty(t, followUps(series, v, lifecycle.Plan{}))
	assert.Empty(t, followUps(series, v, lifecycle.Plan{Freed: []schema.ItemRef{ep("s01e15"), ep("s01e16")}}))

	effs := followUps(series, v, lifecycle.Plan{Freed: []schema.ItemRef{ep("s01e14"), ep("s01e15"), ep("s01e16"), ep("s01e17")}})
	require.Len(t, effs, 1)
	assert.Equal(t, remediation.GrantSearch{Series: series, Items: []schema.ItemRef{ep("s01e14"), ep("s01e17")}, At: now}, effs[0])
}
