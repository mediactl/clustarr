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
package grabplan_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/grabplan"
	"github.com/mediactl/clustarr/app/catalog/worker/search"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/intake"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

var heatParts = []string{
	"Heat.1995.Part1.1080p.BluRay.x264-GRP",
	"Heat.1995.Part2.1080p.BluRay.x264-GRP",
	"Heat.1995.Part3.1080p.BluRay.x264-GRP",
}

// setCandidate is Heat's search answer holding the three posted parts and
// the set the search agent adds for them, as the grab stage reads it: the
// set's candidate.
func setCandidate(t *testing.T) grabplan.Candidate {
	t.Helper()
	target := decision.Target{
		Kind: commonv1.MediaKindMovie, Key: "uid-heat", Monitored: true, Available: true,
		Identity: decision.Identity{Titles: []string{"Heat"}, Year: 1995},
	}
	o := decision.Options{UserInvoked: true, ProtocolsEnabled: map[string]bool{"torrent": true, "usenet": true}}
	ctx := context.Background()
	var rels []commonv1.ReleaseInfo
	for _, title := range heatParts {
		rels = append(rels, rel(title))
	}
	ds := decision.EvaluateRelease(ctx, target, *hdProfile(t), &catalogue.Catalogue{}, rels, o)
	sets := decision.PartSets(ds)
	require.Len(t, sets, 1)
	judged := decision.EvaluateRelease(ctx, target, *hdProfile(t), &catalogue.Catalogue{}, []commonv1.ReleaseInfo{sets[0].Release()}, o)
	ds = append(ds, sets[0].Judge(judged[0]))
	rec := &schema.SearchRecord{Task: "uid-heat", FinishedAt: now, Candidates: search.RankScored(ds, o, nil, 0)}
	for _, c := range grabplan.FromRecord(rec, nil, commonv1.GrabSourceSearch) {
		if len(c.Release.Decision.Parts) > 0 {
			return c
		}
	}
	t.Fatal("the set is not an approved candidate")
	return grabplan.Candidate{}
}

// A set's grab is one entry per part, made in one pass: each its part's
// own release and source, all naming the set.
func TestASetIsGrabbedAsAnEntryPerPart(t *testing.T) {
	c := setCandidate(t)
	p := grabplan.Decide(movieView(t, nil, c))
	require.Len(t, p.NewEntries, 3, "%+v", p.Decisions)
	for i, e := range p.NewEntries {
		require.NotNil(t, e.Set)
		assert.Equal(t, catalogv1alpha1.EntrySet{ID: c.Release.Decision.GUID, Part: int32(i + 1), Of: 3}, *e.Set)
		assert.Equal(t, heatParts[i], e.Release.Title)
		assert.Equal(t, lifecycle.EntryID("heat", "", "guid-"+heatParts[i]), e.ID)
		require.NotNil(t, e.Source.IndexerDownload)
		assert.Equal(t, "guid-"+heatParts[i], e.Source.IndexerDownload.GUID, "each part is fetched by its own GUID")
		assert.Equal(t, commonv1.DownloadPhasePending, e.Phase)
	}
	assert.NotEqual(t, p.NewEntries[0].UID, p.NewEntries[1].UID)
}

// A set is grabbed now or refused: a delay profile never holds it, an
// owner without room for every part refuses it, and a set one of whose
// parts is already a grab of the owner is refused.
func TestASetIsGrabbedWholeOrNotAtAll(t *testing.T) {
	c := setCandidate(t)
	t.Run("never delayed", func(t *testing.T) {
		v := movieView(t, nil, parked(heatRef, "rss-1", c, false, false))
		v.Delay = &catalogv1alpha1.DelayProfileSpec{TorrentDelayMinutes: 60}
		p := grabplan.Decide(v)
		assert.Len(t, p.NewEntries, 3)
		assert.Empty(t, p.Pending)
	})
	t.Run("no room for every part", func(t *testing.T) {
		v := movieView(t, nil, parked(heatRef, "rss-2", c, false, false))
		v.EntryCap = 2
		p := grabplan.Decide(v)
		assert.Empty(t, p.NewEntries)
		assert.Equal(t, intake.Refused, p.Settle["rss-2"])
	})
	t.Run("a set whose parts were lost", func(t *testing.T) {
		lost := c
		lost.Release.Decision.ReleaseInfo = *c.Release.Decision.ReleaseInfo.DeepCopy()
		lost.Release.Decision.Parts = nil // as a status result pruned by an older CRD reads
		p := grabplan.Decide(movieView(t, nil, parked(heatRef, "pick-4", lost, true, true)))
		assert.Empty(t, p.NewEntries, "a set's GUID is nothing to fetch")
	})
	t.Run("a part already grabbed", func(t *testing.T) {
		v := movieView(t, nil, parked(heatRef, "rss-3", c, false, false))
		first := grabplan.Decide(movieView(t, nil, c)).NewEntries[1]
		first.Phase = commonv1.DownloadPhaseFailed
		v.Entries = []catalogv1alpha1.DownloadEntry{first}
		p := grabplan.Decide(v)
		assert.Empty(t, p.NewEntries)
		assert.Equal(t, intake.Refused, p.Settle["rss-3"])
	})
}
