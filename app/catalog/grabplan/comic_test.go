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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/grabplan"
	"github.com/mediactl/clustarr/app/catalog/worker/search"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// The comic fixture is Saga, a Comic in namespace media with four issues,
// under the built-in comic profile's ladder (CBZ the cutoff, CBR, PDF).
// Every candidate is built through the real producers, as the Breaking Bad
// fixture is: the title through release.Parse and pkg/decision's
// EvaluateRelease, ranked by search.RankScored and turned into candidates by
// FromSeasonRecord -- a volume answer (2026-11-10 design D10) -- or
// FromParked.

var sagaRef = schema.ItemRef{Kind: "Comic", Ref: schema.Ref{Namespace: ns, Name: "saga", UID: "uid-saga"}}

// comicProfile is the comic ladder: CBZ (the cutoff), CBR, PDF.
func comicProfile(t *testing.T) *quality.Profile {
	t.Helper()
	var tiers [][]quality.Definition
	for _, name := range []string{"CBZ", "CBR", "PDF"} {
		d, ok := quality.Lookup("comic", name)
		require.True(t, ok, name)
		tiers = append(tiers, []quality.Definition{d})
	}
	return &quality.Profile{
		Tiers: tiers, CutoffIndex: 0, UpgradeAllowed: true, CutoffFormatScore: 10000, MinUpgradeFormatScore: 1,
		ProperPolicy: "preferAndUpgrade", LanguageName: "any", MediaKind: "comic",
	}
}

// issue is one of Saga's issues: monitored and released.
func issue(n string) grabplan.Covered {
	name := "saga-" + n
	return grabplan.Covered{
		Ref:       schema.ItemRef{Kind: "Issue", Ref: schema.Ref{Namespace: ns, Name: name, UID: "uid-" + name}},
		Issue:     n,
		Monitored: true, Wanted: true,
	}
}

func issues(ns ...string) []grabplan.Covered {
	out := make([]grabplan.Covered, 0, len(ns))
	for _, n := range ns {
		out = append(out, issue(n))
	}
	return out
}

func comicView(t *testing.T, covered []grabplan.Covered, cands ...grabplan.Candidate) grabplan.View {
	return grabplan.View{
		Owner: sagaRef, Kind: commonv1.MediaKindComic, Now: now, Monitored: true,
		Profile: comicProfile(t), ProfileRef: "comic",
		Protocols: map[commonv1.Protocol]bool{commonv1.ProtocolTorrent: true, commonv1.ProtocolUsenet: true},
		Covered:   covered, Candidates: cands, EntryCap: 100, PendingCap: 100,
	}
}

// sagaTitle is a Western-style comic release of issue n, a CBZ.
func sagaTitle(n int) string { return fmt.Sprintf("Saga %03d (2012).cbz", n) }

// volumeCandidates are a volume answer's candidates as the grab stage reads
// them (FromSeasonRecord at the volume): each release judged by the search
// agent for the issue it names and approved.
func volumeCandidates(t *testing.T, titles ...string) []grabplan.Candidate {
	t.Helper()
	o := decision.Options{UserInvoked: true, ProtocolsEnabled: map[string]bool{"torrent": true, "usenet": true}}
	rec := &schema.SearchRecord{Task: "uid-saga", FinishedAt: now.Add(-time.Minute)}
	for _, title := range titles {
		var n int
		_, err := fmt.Sscanf(title, "Saga %d (", &n)
		require.NoError(t, err, title)
		target := decision.Target{
			Kind: commonv1.MediaKindIssue, Key: "uid-saga-" + fmt.Sprint(n), Monitored: true, Available: true,
			Identity: decision.Identity{Titles: []string{"Saga"}, Issue: fmt.Sprint(n), Year: 2012},
		}
		ds := decision.EvaluateRelease(context.Background(), target, *comicProfile(t), &catalogue.Catalogue{}, []commonv1.ReleaseInfo{rel(title)}, o)
		scored := search.RankScored(ds, o, nil, 0)
		require.Len(t, scored, 1)
		require.True(t, scored[0].Decision.Approved, "the agent approves %s: %+v", title, scored[0].Decision.Rejections)
		require.Equal(t, fmt.Sprintf("%03d", n), scored[0].Parsed.Issue, "the parsed issue fact rides the record")
		rec.Candidates = append(rec.Candidates, scored[0])
	}
	return grabplan.FromSeasonRecord(rec, 1, commonv1.GrabSourceSearch)
}

// issueEntry is a stored grab in flight covering the issue numbers.
func issueEntry(c grabplan.Candidate, phase commonv1.DownloadPhase, nums ...string) catalogv1alpha1.DownloadEntry {
	info := c.Release.Decision.ReleaseInfo
	return catalogv1alpha1.DownloadEntry{
		ID: "entry-" + info.GUID, UID: "uid-entry-" + info.GUID,
		Release:   grabplan.DownloadRelease(info, c.Release.Score),
		Source:    *c.Source.DeepCopy(),
		GrabbedBy: commonv1.GrabSourceSearch, GrabbedAt: metav1.NewTime(now.Add(-time.Hour)),
		Phase: phase, Issues: nums,
	}
}

// TestAVolumeAnswerCoversTheIssuesItNames is design D10 on the grab side:
// a Comic's volume answer -- the Series' season answer with the season
// layer removed -- covers each Issue by the parsed issue number (001 is
// issue 1), so every approved release is grabbed for its own issue, a
// release of an issue the volume does not hold covers nothing, and a live
// single of an issue refuses another release of it as it does an episode's.
func TestAVolumeAnswerCoversTheIssuesItNames(t *testing.T) {
	v := comicView(t, issues("1", "2", "3", "4"), volumeCandidates(t, sagaTitle(1), sagaTitle(2), sagaTitle(5))...)
	p := grabplan.Decide(v)
	require.Equal(t, []string{sagaTitle(1), sagaTitle(2)}, titles(p), "issue 5 is no issue of the volume: %+v", p.Decisions)
	require.Equal(t, []string{"1"}, p.NewEntries[0].Issues)
	require.Equal(t, []string{"2"}, p.NewEntries[1].Issues)
	require.Equal(t, 1, p.Decisions["refused"], "%+v", p.Decisions)

	t.Run("a live single holds its issue", func(t *testing.T) {
		live := volumeCandidates(t, sagaTitle(3))
		v := comicView(t, issues("1", "2", "3", "4"), volumeCandidates(t, sagaTitle(3))...)
		v.Entries = []catalogv1alpha1.DownloadEntry{issueEntry(live[0], commonv1.DownloadPhaseDownloading, "3")}
		p := grabplan.Decide(v)
		require.Empty(t, titles(p))
		require.Equal(t, 1, p.Decisions["overlapsLive"], "%+v", p.Decisions)
	})
	t.Run("an issue already imported at the cutoff wants nothing", func(t *testing.T) {
		covered := issues("1", "2")
		cbz, ok := quality.Lookup("comic", "CBZ")
		require.True(t, ok)
		covered[0].Current = &decision.Current{Quality: cbz.Quality}
		p := grabplan.Decide(comicView(t, covered, volumeCandidates(t, sagaTitle(1), sagaTitle(2))...))
		require.Equal(t, []string{sagaTitle(2)}, titles(p), "%+v", p.Decisions)
	})
}

// TestAWholeVolumeRanksAsAWholeSeason is design D10's ranking rule: a
// release covering every released issue of the volume carries
// rankscore.WholeSeasonBonus on a Comic as a season pack does on a Series,
// so it is taken ahead of a single scored a little higher, and names every
// issue it brings; the single is then refused as covered.
func TestAWholeVolumeRanksAsAWholeSeason(t *testing.T) {
	covered := issues("1", "2", "3", "4")
	single := volumeCandidates(t, sagaTitle(1))[0]
	single.Release.Score = 100
	pack := parked(sagaRef, "rss-pack", volumeCandidates(t, sagaTitle(1))[0], false, false)
	pack.Release.Score = 0
	pack.Release.Decision.Title, pack.Release.Decision.GUID = "Saga v01 pack", "guid-pack"
	pack.Release.Decision.ReleaseInfo.Title, pack.Release.Decision.ReleaseInfo.GUID = "Saga v01 pack", "guid-pack"
	pack.Release.Decision.ReleaseInfo.InfoHash = fmt.Sprintf("%040d", 7)
	for _, it := range covered {
		pack.Targets = append(pack.Targets, it.Ref)
	}

	p := grabplan.Decide(comicView(t, covered, single, pack))
	require.Equal(t, []string{"Saga v01 pack"}, titles(p), "the whole volume outranks the single's score: %+v", p.Decisions)
	require.Equal(t, []string{"1", "2", "3", "4"}, p.NewEntries[0].Issues)
}
