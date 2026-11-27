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

package remediation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/catalog/backlog"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

func wakes(b *backlog.Book) []string {
	var out []string
	for {
		select {
		case ref := <-b.Wakes():
			out = append(out, ref.Name)
		default:
			return out
		}
	}
}

// The episodes a pack was grabbed for and did not hold are each granted a
// search (GrantSearch). Two or more are their Series' to ask for as a
// season, which is where another pack is found, so the Series holds the
// season's grant and is the one woken; one episode searches alone.
func TestTheEpisodesAPackDidNotHoldAreGrantedASearch(t *testing.T) {
	at := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	series := schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: "media", Name: "show", UID: "u-show"}}
	ep := func(n string) schema.ItemRef {
		return schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Namespace: "media", Name: "show-" + n, UID: "u-" + n}}
	}

	t.Run("a season's worth", func(t *testing.T) {
		ir := &ItemReconciler{Backlog: backlog.NewBook()}
		require.NoError(t, ir.runItemEffect(context.Background(), nil,
			GrantSearch{Series: series, Items: []schema.ItemRef{ep("s01e14"), ep("s01e15")}, At: at}))
		for _, uid := range []string{"u-s01e14", "u-s01e15"} {
			g, ok := ir.Backlog.Granted(uid, at.Add(time.Hour))
			require.True(t, ok, uid)
			assert.Equal(t, at.Add(FollowUpLapse), g.Until)
		}
		assert.True(t, ir.Backlog.SeasonGranted("u-show", at.Add(time.Hour)))
		assert.False(t, ir.Backlog.SeasonGranted("u-show", at.Add(FollowUpLapse)), "the grant lapses")
		assert.Equal(t, []string{"show"}, wakes(ir.Backlog))
	})
	t.Run("one episode", func(t *testing.T) {
		ir := &ItemReconciler{Backlog: backlog.NewBook()}
		require.NoError(t, ir.runItemEffect(context.Background(), nil,
			GrantSearch{Series: series, Items: []schema.ItemRef{ep("s01e26")}, At: at}))
		_, ok := ir.Backlog.Granted("u-s01e26", at.Add(time.Hour))
		assert.True(t, ok)
		assert.False(t, ir.Backlog.SeasonGranted("u-show", at.Add(time.Hour)))
		assert.Equal(t, []string{"show-s01e26"}, wakes(ir.Backlog))
	})
	t.Run("no book grants nothing", func(t *testing.T) {
		ir := &ItemReconciler{}
		require.NoError(t, ir.runItemEffect(context.Background(), nil, GrantSearch{Series: series, Items: []schema.ItemRef{ep("s01e26")}, At: at}))
	})
}
