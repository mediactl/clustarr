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

package importplan_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importplan"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// seasonOfFour gives The Wire the four episodes of a season a pack may
// hold half of.
func seasonOfFour(wd *world) {
	for n := int32(3); n <= 4; n++ {
		name := fmt.Sprintf("the-wire-s01e%02d", n)
		wd.add(&catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(name + "-uid")},
			Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "the-wire", SeasonNumber: 1, EpisodeNumber: n},
			Status:     catalogv1alpha1.EpisodeStatus{Title: fmt.Sprintf("Episode %d", n)},
		})
	}
}

// grabbedFor makes g a grab for the season 1 episodes numbers names: the
// entry's episodes, and its targets as the stage reads them from those.
func (g *grab) grabbedFor(numbers ...int32) {
	g.in.Entry.Episodes, g.in.Targets = nil, nil
	for _, n := range numbers {
		name := fmt.Sprintf("the-wire-s01e%02d", n)
		g.in.Entry.Episodes = append(g.in.Entry.Episodes, catalogv1alpha1.EpisodeNumber{Season: 1, Number: n})
		g.in.Targets = append(g.in.Targets, schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Namespace: ns, Name: name, UID: name + "-uid"}})
	}
}

func pack(wd *world, episodes ...int) string {
	files := map[string]int64{}
	for _, n := range episodes {
		files[fmt.Sprintf("The.Wire.S01.1080p.BluRay.x264-GRP/The.Wire.S01E%02d.1080p.BluRay.x264-GRP.mkv", n)] = big
	}
	return wd.payload(files)
}

func names(refs []schema.ItemRef) []string {
	var out []string
	for _, r := range refs {
		out = append(out, r.Name)
	}
	return out
}

func eps(numbers ...int32) []catalogv1alpha1.EpisodeNumber {
	var out []catalogv1alpha1.EpisodeNumber
	for _, n := range numbers {
		out = append(out, catalogv1alpha1.EpisodeNumber{Season: 1, Number: n})
	}
	return out
}

// A pack accounts for the files it holds (owner, 2026-11-27): one grabbed
// for a season's second half that holds its first names the first half from
// its inspection on, and the second half is free for another release. On
// talos three "S01" packs of a 26-episode season each held episodes 1 to
// 13 and went on covering 14 to 26 for the 24 hours of their hold.
func TestAPackAccountsForTheEpisodesItsFilesAre(t *testing.T) {
	const title = "The.Wire.S01.1080p.BluRay.x264-GRP"
	tests := []struct {
		name     string
		grabbed  []int32
		holds    []int
		episodes []catalogv1alpha1.EpisodeNumber
		freed    []string
	}{
		{"none of the episodes it was grabbed for", []int32{3, 4}, []int{1, 2}, eps(1, 2), []string{"the-wire-s01e03", "the-wire-s01e04"}},
		{"some of them", []int32{2, 3}, []int{1, 2}, eps(2), []string{"the-wire-s01e03"}},
		{"all of them and more", []int32{3, 4}, []int{1, 2, 3, 4}, nil, nil},
		{"exactly them", []int32{1, 2}, []int{1, 2}, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wd := newWorld(t)
			seasonOfFour(wd)
			g := wd.grab(commonv1.MediaKindSeries, "the-wire", title, pack(wd, tt.holds...))
			g.grabbedFor(tt.grabbed...)
			dec := g.inspected()
			assert.Equal(t, tt.episodes, dec.Episodes, dec.Summary.Message)
			assert.ElementsMatch(t, tt.freed, names(dec.Freed))
		})
	}
}

// What it does not decide: a pack with a file nobody could attribute, which
// may be any episode it names; a person's target; and an entry whose files
// an execute has begun to move, since a later inspection lists what is
// left of the payload.
func TestAPackKeepsItsEpisodesWhereItsFilesDoNotSay(t *testing.T) {
	const title = "The.Wire.S01.1080p.BluRay.x264-GRP"
	t.Run("a file nobody could attribute", func(t *testing.T) {
		wd := newWorld(t)
		seasonOfFour(wd)
		root := wd.payload(map[string]int64{
			title + "/The.Wire.S01E01.1080p.BluRay.x264-GRP.mkv": big,
			title + "/1cde88d8054f79f612c3b7b3f106cbfb.mkv":      big,
		})
		g := wd.grab(commonv1.MediaKindSeries, "the-wire", title, root)
		g.grabbedFor(3, 4)
		dec := g.inspected()
		assert.Nil(t, dec.Episodes, dec.Summary.Message)
		assert.Empty(t, dec.Freed)
	})
	t.Run("a person's target", func(t *testing.T) {
		wd := newWorld(t)
		seasonOfFour(wd)
		g := wd.grab(commonv1.MediaKindSeries, "the-wire", title, pack(wd, 1, 2))
		g.grabbedFor(3, 4)
		require.NotNil(t, g.inspected().Episodes)
		g.dec.Summary.Target = "episode/the-wire-s01e03"
		assert.Nil(t, g.decide().Episodes)
	})
	t.Run("an import under way", func(t *testing.T) {
		wd := newWorld(t)
		seasonOfFour(wd)
		g := wd.grab(commonv1.MediaKindSeries, "the-wire", title, pack(wd, 1, 2, 3))
		g.grabbedFor(1, 2, 3)
		require.Nil(t, g.inspected().Episodes)
		// The execute has moved the files; an inspection made from here on
		// lists what is left of the payload, which says nothing of what
		// the pack held.
		left := func(in *importplan.Input) {
			r := *in.Inspect
			insp := *r.Inspect
			insp.Files = insp.Files[len(insp.Files)-1:]
			r.Inspect, in.Inspect = &insp, &r
		}
		dec := g.executed(left)
		require.NotNil(t, g.record(schema.ImportSubExecute))
		assert.Nil(t, dec.Episodes, dec.Summary.Message)
		assert.Empty(t, dec.Freed)
	})
}
