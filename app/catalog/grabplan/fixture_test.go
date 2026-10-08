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
	"github.com/mediactl/clustarr/app/intake"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// The fixture is Breaking Bad, a Series in namespace media, and a movie,
// Heat. Every candidate is built through the real producers: the release
// title through release.Parse and pkg/decision's EvaluateRelease (the
// search agent's half), ranked and recorded by the search agent's own
// search.RankScored, then turned into candidates by FromRecord,
// FromSeasonRecord or FromParked -- so a record carries exactly the parsed
// facts and scene-mapped coverage an agent would send.

var now = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

const (
	ns      = "media"
	indexer = "nzbgeek"
)

var (
	bbRef   = schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: ns, Name: "breaking-bad", UID: "uid-bb"}}
	heatRef = schema.ItemRef{Kind: "Movie", Ref: schema.Ref{Namespace: ns, Name: "heat", UID: "uid-heat"}}
)

// hdProfile is Bluray-1080p (the cutoff), WEBDL-1080p, HDTV-720p, best
// first, upgrades allowed.
func hdProfile(t *testing.T) *quality.Profile {
	t.Helper()
	var tiers [][]quality.Definition
	for _, name := range []string{"Bluray-1080p", "WEBDL-1080p", "HDTV-720p"} {
		d, ok := quality.Lookup("video", name)
		require.True(t, ok, name)
		tiers = append(tiers, []quality.Definition{d})
	}
	return &quality.Profile{
		Tiers: tiers, CutoffIndex: 0, UpgradeAllowed: true, CutoffFormatScore: 10000, MinUpgradeFormatScore: 1,
		ProperPolicy: "preferAndUpgrade", LanguageName: "any",
	}
}

func q(t *testing.T, name string) commonv1.Quality {
	t.Helper()
	d, ok := quality.Lookup("video", name)
	require.True(t, ok, name)
	return d.Quality
}

// episode is one of Breaking Bad's episodes: monitored and aired.
func episode(season, n int32) grabplan.Covered {
	name := fmt.Sprintf("breaking-bad-s%02de%02d", season, n)
	return grabplan.Covered{
		Ref:    schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Namespace: ns, Name: name, UID: "uid-" + name}},
		Season: season, Number: n, Monitored: true, Wanted: true,
	}
}

// season is n aired episodes of a season.
func season(s int32, n int) []grabplan.Covered {
	out := make([]grabplan.Covered, 0, n)
	for e := int32(1); e <= int32(n); e++ {
		out = append(out, episode(s, e))
	}
	return out
}

func seriesView(t *testing.T, covered []grabplan.Covered, cands ...grabplan.Candidate) grabplan.View {
	return grabplan.View{
		Owner: bbRef, Kind: commonv1.MediaKindSeries, Now: now, Monitored: true,
		Profile: hdProfile(t), ProfileRef: "hd-1080p",
		Protocols: map[commonv1.Protocol]bool{commonv1.ProtocolTorrent: true, commonv1.ProtocolUsenet: true},
		Covered:   covered, Candidates: cands, EntryCap: 100, PendingCap: 100,
	}
}

func movieView(t *testing.T, cur *decision.Current, cands ...grabplan.Candidate) grabplan.View {
	return grabplan.View{
		Owner: heatRef, Kind: commonv1.MediaKindMovie, Now: now, Monitored: true,
		Profile: hdProfile(t), ProfileRef: "hd-1080p",
		Protocols:  map[commonv1.Protocol]bool{commonv1.ProtocolTorrent: true},
		Covered:    []grabplan.Covered{{Ref: heatRef, Monitored: true, Wanted: true, Current: cur}},
		Candidates: cands, EntryCap: 100, PendingCap: 1,
	}
}

// rel is a release as an indexer answers it: GUID per title, an info hash
// per title, so a tombstone matches it by either.
func rel(title string) commonv1.ReleaseInfo {
	return commonv1.ReleaseInfo{
		GUID: "guid-" + title, IndexerRef: indexer, Title: title, Protocol: commonv1.ProtocolTorrent,
		InfoHash: fmt.Sprintf("%040x", len(title)*7919+int(title[len(title)-1])),
	}
}

// record evaluates titles against target as the search agent does and
// records the ranked answer.
func record(t *testing.T, target decision.Target, rels ...commonv1.ReleaseInfo) *schema.SearchRecord {
	t.Helper()
	o := decision.Options{UserInvoked: true, ProtocolsEnabled: map[string]bool{"torrent": true, "usenet": true}}
	ds := decision.EvaluateRelease(context.Background(), target, *hdProfile(t), &catalogue.Catalogue{}, rels, o)
	return &schema.SearchRecord{
		Task: target.Key, FinishedAt: now.Add(-time.Minute),
		Candidates: search.RankScored(ds, o, nil, 0),
	}
}

// seasonSearch is a season search's target of Breaking Bad.
func seasonSearch(s int, eps int, scene []decision.SceneMapping) decision.Target {
	numbers := make([]int, 0, eps)
	for e := 1; e <= eps; e++ {
		numbers = append(numbers, e)
	}
	return decision.Target{
		Kind: commonv1.MediaKindSeries, Key: "uid-bb", Monitored: true, Available: true,
		Identity: decision.Identity{Titles: []string{"Breaking Bad"}, Season: s, Episodes: numbers, SeasonSearch: true, SceneMappings: scene},
	}
}

// seasonCandidates are a season answer's candidates, as the grab stage
// reads them (FromSeasonRecord).
func seasonCandidates(t *testing.T, s int32, eps int, titles ...string) []grabplan.Candidate {
	t.Helper()
	rels := make([]commonv1.ReleaseInfo, 0, len(titles))
	for _, title := range titles {
		rels = append(rels, rel(title))
	}
	rec := record(t, seasonSearch(int(s), eps, nil), rels...)
	require.Len(t, rec.Candidates, len(titles))
	for _, c := range rec.Candidates {
		require.True(t, c.Decision.Approved, "the agent approves %s: %+v", c.Decision.Title, c.Decision.Rejections)
	}
	return grabplan.FromSeasonRecord(rec, s, commonv1.GrabSourceSearch)
}

// movieCandidate is Heat's search answer, one approved release.
func movieCandidate(t *testing.T, title string) grabplan.Candidate {
	t.Helper()
	target := decision.Target{
		Kind: commonv1.MediaKindMovie, Key: "uid-heat", Monitored: true, Available: true,
		Identity: decision.Identity{Titles: []string{"Heat"}, Year: 1995},
	}
	cs := grabplan.FromRecord(record(t, target, rel(title)), nil, commonv1.GrabSourceSearch)
	require.Len(t, cs, 1, "the agent approves %s", title)
	return cs[0]
}

// parked is an intake candidate as the Search controller publishes a pick
// (manual) or as the RSS matcher publishes a match.
func parked(owner schema.ItemRef, msgID string, c grabplan.Candidate, manual, override bool) grabplan.Candidate {
	origin, by := schema.CandidateOriginRSS, commonv1.GrabSourceRSS
	var searchRef *schema.Ref
	if manual {
		origin, by = schema.CandidateOriginInteractive, commonv1.GrabSourceInteractive
		searchRef = &schema.Ref{Namespace: ns, Name: "pick"}
	}
	p := intake.Parked{MsgID: msgID, At: now, Candidate: schema.Candidate{
		Owner: owner, Origin: origin, SearchRef: searchRef, Manual: manual, Override: override,
		GrabbedBy: by, Release: c.Release, At: now, Season: c.Season, Targets: c.Targets,
	}}
	return grabplan.FromParked(p)
}

// liveEntry is a stored grab of a release in flight (or a tombstone),
// covering eps: the release as the downloads stage keeps it.
func liveEntry(c grabplan.Candidate, phase commonv1.DownloadPhase, eps ...catalogv1alpha1.EpisodeNumber) catalogv1alpha1.DownloadEntry {
	info := c.Release.Decision.ReleaseInfo
	return catalogv1alpha1.DownloadEntry{
		ID: "entry-" + info.GUID, UID: "uid-entry-" + info.GUID,
		Release:   grabplan.DownloadRelease(info, c.Release.Score),
		Source:    *c.Source.DeepCopy(),
		GrabbedBy: commonv1.GrabSourceSearch, GrabbedAt: metav1.NewTime(now.Add(-time.Hour)),
		Phase: phase, Episodes: eps,
	}
}

func ep(s, n int32) catalogv1alpha1.EpisodeNumber {
	return catalogv1alpha1.EpisodeNumber{Season: s, Number: n}
}

// titles are the plan's new entries' release titles.
func titles(p grabplan.Plan) []string {
	out := make([]string, 0, len(p.NewEntries))
	for _, e := range p.NewEntries {
		out = append(out, e.Release.Title)
	}
	return out
}
