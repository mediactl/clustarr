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

package search

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	worker "github.com/mediactl/clustarr/app/catalog/worker/search"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// The season fixture is Breaking Bad's season 1 under the built-in
// hd-bluray-web profile (Bluray-1080p the cutoff, WEB 1080p, Bluray-720p),
// read through a client that indexes Episodes by their Series as the
// manager's cache does. Every result is the search agent's own: the title
// through pkg/decision's EvaluateRelease against a season search, ranked
// and recorded by worker.RankScored.

var seasonNow = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

const seasonNS = "media"

// builtinProfile is the QualityProfile CR the catalog seeds for name.
func builtinProfile(t *testing.T, name string) *catalogv1alpha1.QualityProfile {
	t.Helper()
	doc, err := catalogue.ProfileFS().ReadFile("data/profiles/" + name + ".json")
	require.NoError(t, err)
	seeds, err := quality.DecodeProfileSeeds(doc)
	require.NoError(t, err)
	require.Len(t, seeds, 1)
	return &catalogv1alpha1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: seeds[0].Spec}
}

func bbSeries(entries ...catalogv1alpha1.DownloadEntry) *catalogv1alpha1.Series {
	return &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "breaking-bad", Namespace: seasonNS, UID: "uid-bb"},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 81189, QualityProfileRef: "hd-bluray-web", RootFolderRef: "tv"},
		Status: catalogv1alpha1.SeriesStatus{
			Metadata:  &catalogv1alpha1.SeriesMetadata{Title: "Breaking Bad", OriginalLanguage: "en"},
			Downloads: entries,
		},
	}
}

// bbEpisode is an episode of Breaking Bad that aired airedAgo before
// seasonNow (negative: not yet aired), with absolute number abs (0: none).
func bbEpisode(season, n int32, airedAgo time.Duration, abs int32) *catalogv1alpha1.Episode {
	at := metav1.NewTime(seasonNow.Add(-airedAgo))
	ep := &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("breaking-bad-s%02de%02d", season, n), Namespace: seasonNS},
		Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "breaking-bad", SeasonNumber: season, EpisodeNumber: n},
		Status:     catalogv1alpha1.EpisodeStatus{AirDate: &at},
	}
	if abs > 0 {
		ep.Status.AbsoluteNumber = &abs
	}
	return ep
}

// withFile gives ep an imported file of quality name, transcoded or not.
func withFile(t *testing.T, ep *catalogv1alpha1.Episode, name string) (*catalogv1alpha1.Episode, *catalogv1alpha1.MediaFile) {
	t.Helper()
	d, ok := quality.Lookup("video", name)
	require.True(t, ok, name)
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: ep.Name + "-file", Namespace: seasonNS},
		Spec: catalogv1alpha1.MediaFileSpec{
			Path: "/data/tv/" + ep.Name + ".mkv", Quality: d.Quality,
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: ep.Name},
		},
	}
	ep.Status.HasFile, ep.Status.FileRef = true, ptr.To(mf.Name)
	return ep, mf
}

func seasonReconciler(t *testing.T, objs ...client.Object) *Reconciler {
	t.Helper()
	objs = append(objs, builtinProfile(t, "hd-bluray-web"))
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).
		WithIndex(&catalogv1alpha1.Episode{}, series.EpisodeBySeriesRefIndex, func(o client.Object) []string {
			return []string{o.(*catalogv1alpha1.Episode).Spec.SeriesRef}
		}).
		WithStatusSubresource(&catalogv1alpha1.Search{}).Build()
	return &Reconciler{Client: c, Scheme: c.Scheme(), Clock: clockwork.NewFakeClockAt(seasonNow)}
}

func interactiveSeasonSearch(season int32) *catalogv1alpha1.Search {
	return &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "bb-s1", Namespace: seasonNS, UID: "uid-search"},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: "breaking-bad"}, Season: &season,
		},
	}
}

// seasonAnswer is the search agent's answer to a season search of season
// over episodes 1..eps (absolute numbers abs, when the series has them).
func seasonAnswer(t *testing.T, season, eps int, abs []int, titles ...string) []schema.ScoredRelease {
	t.Helper()
	numbers := make([]int, 0, eps)
	for e := 1; e <= eps; e++ {
		numbers = append(numbers, e)
	}
	target := decision.Target{
		Kind: commonv1.MediaKindSeries, Monitored: true, Available: true, OriginalLanguageTag: "en",
		Identity: decision.Identity{Titles: []string{"Breaking Bad"}, Season: season, Episodes: numbers, Absolute: abs, SeasonSearch: true},
	}
	rels := make([]commonv1.ReleaseInfo, 0, len(titles))
	for _, title := range titles {
		rels = append(rels, commonv1.ReleaseInfo{GUID: "guid-" + title, IndexerRef: "nzbgeek", Title: title, Protocol: commonv1.ProtocolTorrent})
	}
	o := decision.Options{UserInvoked: true, ProtocolsEnabled: map[string]bool{"torrent": true}}
	p := builtinProfile(t, "hd-bluray-web")
	prof, errs := quality.FromCRD(p, catalogue.LoadedCatalogue())
	require.Empty(t, errs)
	return worker.RankScored(decision.EvaluateRelease(context.Background(), target, prof, catalogue.LoadedCatalogue(), rels, o), o, nil, 0)
}

// byTitle indexes results by release title.
func byTitle(rs []commonv1.ReleaseDecision) map[string]commonv1.ReleaseDecision {
	out := map[string]commonv1.ReleaseDecision{}
	for _, r := range rs {
		out[r.Title] = r
	}
	return out
}

func rejectedAs(t *testing.T, rd commonv1.ReleaseDecision, r decision.Reason) {
	t.Helper()
	require.False(t, rd.Approved, rd.Title)
	for _, rej := range rd.Rejections {
		if r.Of(rej) {
			return
		}
	}
	t.Fatalf("%s: want a %s rejection, got %+v", rd.Title, r.Code, rd.Rejections)
}

// TestBandedPutsOtherTitlesLastAndCountsThem is series search design §8.2:
// approved, then rejected, then other titles, each band in the agent's
// order; the cap drops other titles first; Rank is the 1-based position.
func TestBandedPutsOtherTitlesLastAndCountsThem(t *testing.T) {
	approved := func(g string) commonv1.ReleaseDecision {
		return commonv1.ReleaseDecision{ReleaseInfo: commonv1.ReleaseInfo{GUID: g}, Approved: true}
	}
	rejected := func(g string, r decision.Reason) commonv1.ReleaseDecision {
		return commonv1.ReleaseDecision{ReleaseInfo: commonv1.ReleaseInfo{GUID: g}, Rejections: []commonv1.Rejection{{Reason: r.Code + ": x", Type: r.Type}}}
	}
	in := []commonv1.ReleaseDecision{
		rejected("other-1", decision.ReasonWrongItem), approved("a1"), rejected("r1", decision.ReasonQualityNotWanted),
		rejected("other-2", decision.ReasonUnableToParse), approved("a2"), rejected("r2", decision.ReasonFullSeason),
		rejected("other-3", decision.ReasonUnknownItem),
	}
	guids := func(rs []commonv1.ReleaseDecision) []string {
		var out []string
		for i, r := range rs {
			require.EqualValues(t, i+1, r.Rank)
			out = append(out, r.GUID)
		}
		return out
	}
	out, other := banded(in, 0)
	require.Equal(t, []string{"a1", "a2", "r1", "r2", "other-1", "other-2", "other-3"}, guids(out))
	require.EqualValues(t, 3, other)

	capped, other := banded(in, 5)
	require.Equal(t, []string{"a1", "a2", "r1", "r2", "other-1"}, guids(capped), "the cap drops other titles first")
	require.EqualValues(t, 1, other)

	none, other := banded(in[1:3], 0)
	require.Equal(t, []string{"a1", "r1"}, guids(none))
	require.Zero(t, other)
}

// TestASeasonResultIsRejectedAsThePlannerWould is design §8.1: an
// interactive season search shows each result as the Series' grab planner
// would judge it -- a pack overlapping a live grab QueueHigherPreference,
// a pack of a season still airing FullSeasonNotAired, a pack with some
// episodes already at the cutoff approved (the planner narrows it).
func TestASeasonResultIsRejectedAsThePlannerWould(t *testing.T) {
	const pack = "Breaking.Bad.S01.720p.BluRay.x264-GRP"
	const single = "Breaking.Bad.S01E03.720p.BluRay.x264-GRP"
	answer := seasonAnswer(t, 1, 5, nil, pack, single)
	aired := func() []client.Object {
		var out []client.Object
		for n := int32(1); n <= 5; n++ {
			out = append(out, bbEpisode(1, n, time.Duration(6-n)*7*24*time.Hour, 0))
		}
		return out
	}

	t.Run("a pack overlapping a live grab", func(t *testing.T) {
		live := liveSeriesEntry(t, "Breaking.Bad.S01E02.720p.BluRay.x264-OTHER", ep(1, 2))
		r := seasonReconciler(t, append(aired(), bbSeries(live))...)
		got, _ := r.seasonItemState(context.Background(), interactiveSeasonSearch(1), answer)
		res := byTitle(got)
		rejectedAs(t, res[pack], decision.ReasonQueueHigherPreference)
		require.True(t, res[single].Approved, "a single of another episode does not overlap it: %+v", res[single].Rejections)
	})
	t.Run("a pack of a season still airing", func(t *testing.T) {
		objs := aired()
		objs[4] = bbEpisode(1, 5, -7*24*time.Hour, 0)
		r := seasonReconciler(t, append(objs, bbSeries())...)
		got, _ := r.seasonItemState(context.Background(), interactiveSeasonSearch(1), answer)
		res := byTitle(got)
		rejectedAs(t, res[pack], decision.ReasonFullSeasonNotAired)
		require.True(t, res[single].Approved)
	})
	t.Run("a pack with some episodes at the cutoff", func(t *testing.T) {
		objs := aired()
		e1, f1 := withFile(t, objs[0].(*catalogv1alpha1.Episode), "Bluray-1080p")
		e3, f3 := withFile(t, objs[2].(*catalogv1alpha1.Episode), "Bluray-1080p")
		objs[0], objs[2] = e1, e3
		r := seasonReconciler(t, append(objs, bbSeries(), f1, f3)...)
		got, _ := r.seasonItemState(context.Background(), interactiveSeasonSearch(1), answer)
		res := byTitle(got)
		require.True(t, res[pack].Approved, "the planner narrows it to E02, E04 and E05: %+v", res[pack].Rejections)
		rejectedAs(t, res[single], decision.ReasonExistingHigherPreference)
	})
	t.Run("another title is left as the agent judged it", func(t *testing.T) {
		wrong := seasonAnswer(t, 1, 5, nil, "Better.Call.Saul.S01.720p.BluRay.x264-GRP")
		r := seasonReconciler(t, append(aired(), bbSeries())...)
		got, other := r.seasonItemState(context.Background(), interactiveSeasonSearch(1), wrong)
		require.EqualValues(t, 1, other)
		rejectedAs(t, got[0], decision.ReasonWrongItem)
		require.Len(t, got[0].Rejections, 1)
	})
}

// TestCoversSeasonMarksPacksAndBatches is design §8.1: a release covering
// every aired episode of the searched season -- a full-season pack, or an
// anime batch by absolute numbers -- is CoversSeason; a single and a batch
// short of an aired episode are not, and an unaired episode is not needed.
func TestCoversSeasonMarksPacksAndBatches(t *testing.T) {
	var objs []client.Object
	for n := int32(1); n <= 4; n++ {
		objs = append(objs, bbEpisode(1, n, time.Duration(5-n)*7*24*time.Hour, n))
	}
	objs = append(objs, bbEpisode(1, 5, -7*24*time.Hour, 5), bbSeries())
	r := seasonReconciler(t, objs...)
	const (
		pack    = "Breaking.Bad.S01.720p.BluRay.x264-GRP"
		single  = "Breaking.Bad.S01E02.720p.BluRay.x264-GRP"
		batch   = "[Group] Breaking Bad - 01-04 (1080p) [ABCDEF12].mkv"
		partial = "[Group] Breaking Bad - 01-03 (1080p) [ABCDEF12].mkv"
	)
	answer := seasonAnswer(t, 1, 5, []int{1, 2, 3, 4, 5}, pack, single, batch, partial)
	got, _ := r.seasonItemState(context.Background(), interactiveSeasonSearch(1), answer)
	res := byTitle(got)
	require.Len(t, res, 4)
	require.True(t, res[pack].CoversSeason)
	require.True(t, res[batch].CoversSeason, "every aired episode by absolute number; E05 has not aired")
	require.False(t, res[single].CoversSeason)
	require.False(t, res[partial].CoversSeason, "E04 aired and the batch lacks it")
}

func ep(s, n int32) catalogv1alpha1.EpisodeNumber {
	return catalogv1alpha1.EpisodeNumber{Season: s, Number: n}
}

// liveSeriesEntry is a Series' grab of title in flight, covering eps.
func liveSeriesEntry(t *testing.T, title string, eps ...catalogv1alpha1.EpisodeNumber) catalogv1alpha1.DownloadEntry {
	t.Helper()
	sr := seasonAnswer(t, 1, 5, nil, title)
	require.Len(t, sr, 1)
	info := sr[0].Decision.ReleaseInfo
	return catalogv1alpha1.DownloadEntry{
		ID: "entry-" + info.GUID, UID: "uid-entry",
		Release: catalogv1alpha1.DownloadRelease{
			Title: info.Title, GUID: info.GUID, IndexerRef: info.IndexerRef, Protocol: info.Protocol, Quality: info.Quality,
		},
		GrabbedBy: commonv1.GrabSourceSearch, GrabbedAt: metav1.NewTime(seasonNow.Add(-time.Hour)),
		Phase: commonv1.DownloadPhaseDownloading, Episodes: eps,
	}
}
