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

package artwork

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/overlayplan"
	"github.com/mediactl/clustarr/pkg/dubs"
)

// The overlay stage's decision for an anime, the item read through the
// MAL-Dubs index as the manager carries it. A dubbed anime with no rating
// is asked for its mark; one that leaves the list has it removed; and a
// manager that plans before it has read the list -- when the same anime
// reads as not dubbed -- leaves the poster and its status as they are,
// rather than having every marked poster removed and drawn again at each
// start.
func TestTheOverlayStageHoldsAnAnimeUntilTheDubListIsRead(t *testing.T) {
	lbls := map[string]string{"overlay": "on"}
	profiles := []catalogv1alpha1.OverlayProfile{{
		ObjectMeta: metav1.ObjectMeta{Name: "metascore", Namespace: "anime"},
		Spec:       catalogv1alpha1.OverlayProfileSpec{Selector: &metav1.LabelSelector{MatchLabels: lbls}},
	}}
	plan := func(ix *dubs.Index, recorded *catalogv1alpha1.OverlayEntry) (overlayplan.Item, overlayplan.Want) {
		s := &catalogv1alpha1.Series{
			ObjectMeta: metav1.ObjectMeta{Name: "eighty-six", Namespace: "anime", Labels: lbls},
			Spec:       catalogv1alpha1.SeriesSpec{SeriesType: catalogv1alpha1.SeriesTypeAnime},
			Status: catalogv1alpha1.SeriesStatus{
				Metadata: &catalogv1alpha1.SeriesMetadata{ExternalIDs: map[string]string{"mal": "41457"}},
				Overlay:  recorded,
			},
		}
		it, err := overlayplan.ItemOf(dubs.WithIndex(context.Background(), ix), s)
		require.NoError(t, err)
		return it, overlayplan.Plan(it, profiles, "orig").WithCompact(it, "")
	}

	read := dubs.NewIndex()
	read.Set(dubs.Record{Dubbed: []int{41457}})
	_, marked := plan(read, nil)
	require.NotNil(t, marked.Profile, "a dubbed anime wants its mark")
	drawn := &catalogv1alpha1.OverlayEntry{ProfileRef: "metascore", Digest: "jpeg", RenderedFrom: marked.InputsDigest}

	t.Run("a dubbed anime without an overlay is asked for one", func(t *testing.T) {
		it, want := plan(read, nil)
		status, render, token := decide(it, want, nil, false, false, false)
		assert.Nil(t, status)
		assert.True(t, render)
		assert.Equal(t, marked.InputsDigest+"+"+marked.Compact.InputsDigest, token)
	})
	t.Run("a marked poster is left alone", func(t *testing.T) {
		it, want := plan(read, drawn)
		status, render, _ := decide(it, want, drawn, true, true, false)
		assert.Equal(t, drawn, status)
		assert.False(t, render)
	})
	t.Run("an anime that left the list loses its mark", func(t *testing.T) {
		gone := dubs.NewIndex()
		gone.Set(dubs.Record{})
		it, want := plan(gone, drawn)
		status, render, token := decide(it, want, drawn, true, true, false)
		assert.Nil(t, status)
		assert.True(t, render)
		assert.Equal(t, overlayplan.RenderNone, token)
	})
	t.Run("before the list is read the marked poster stands", func(t *testing.T) {
		it, want := plan(dubs.NewIndex(), drawn)
		require.Nil(t, want.Profile, "unread, the anime plans as not dubbed: the plan is not to be acted on")
		status, render, _ := decide(it, want, drawn, true, true, false)
		assert.Equal(t, drawn, status, "status goes on saying what the object does")
		assert.False(t, render, "no task removes the overlay")
	})
	// The compact rendition (the compact-posters design, 2026-11-27) is
	// the renderer's too: a task is due when it is stale though the regular
	// overlay is right, status.overlay says what it said, and the task's
	// token carries both digests so a task for the compact one alone is
	// not taken for the one already sent.
	t.Run("a stale compact rendition asks for a render and leaves status alone", func(t *testing.T) {
		it, want := plan(read, drawn)
		want = want.WithCompact(it, "")
		status, render, token := decide(it, want, drawn, true, true, true)
		assert.Equal(t, drawn, status)
		assert.True(t, render)
		assert.Equal(t, want.InputsDigest+"+"+want.Compact.InputsDigest, token)

		_, render, same := decide(it, want, drawn, true, true, false)
		assert.False(t, render, "both right: nothing is due")
		assert.Equal(t, token, same)
		_, _, moved := decide(it, want.WithCompact(it, "compact-poster"), drawn, true, true, true)
		assert.NotEqual(t, token, moved, "another compact digest is another task")
	})
	t.Run("a compact rendition left behind by an item that wants no overlay is removed", func(t *testing.T) {
		gone := dubs.NewIndex()
		gone.Set(dubs.Record{})
		it, want := plan(gone, nil)
		status, render, token := decide(it, want, nil, false, false, true)
		assert.Nil(t, status)
		assert.True(t, render)
		assert.Equal(t, overlayplan.RenderNone, token)
	})
	t.Run("before the list is read a stale compact rendition asks nothing", func(t *testing.T) {
		it, want := plan(dubs.NewIndex(), drawn)
		_, render, _ := decide(it, want, drawn, true, true, true)
		assert.False(t, render)
	})
	t.Run("before the list is read an object of an older metadata version keeps its status", func(t *testing.T) {
		it, want := plan(dubs.NewIndex(), drawn)
		status, render, _ := decide(it, want, nil, true, false, false)
		assert.Equal(t, drawn, status)
		assert.False(t, render)
	})
}
