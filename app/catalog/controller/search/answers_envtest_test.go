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

package search_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	worker "github.com/mediactl/clustarr/app/catalog/worker/search"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// recFixture is the Search controller over records: the search agent's
// answers are written to an in-memory clustarr-searches (the topology
// Ensure the manager runs) through the agent's own records.Writer, and the
// reconciler reads them as the manager wires it.
type recFixture struct {
	*fixture
	w *records.Writer[*schema.SearchRecord]
}

func newRecFixture(t *testing.T, ns string) *recFixture {
	t.Helper()
	f := newFixture(t, ns)
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(t.Context(), events.Default()))
	kv := bus.KV(events.BucketSearches)
	f.r.Searches = records.NewReader(kv, agentrecords.Searches())
	ensureProfile(t, f.c)
	return &recFixture{fixture: f, w: records.NewWriter(kv, agentrecords.Searches(), "search-agent-0", "test")}
}

// ensureProfile creates the built-in hd-bluray-web QualityProfile (cluster
// scoped, shared by every test in the package).
func ensureProfile(t *testing.T, c client.Client) {
	t.Helper()
	doc, err := catalogue.ProfileFS().ReadFile("data/profiles/hd-bluray-web.json")
	require.NoError(t, err)
	seeds, err := quality.DecodeProfileSeeds(doc)
	require.NoError(t, err)
	err = c.Create(context.Background(), &catalogv1alpha1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: "hd-bluray-web"}, Spec: seeds[0].Spec})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}
}

func hdProfile(t *testing.T) quality.Profile {
	t.Helper()
	doc, err := catalogue.ProfileFS().ReadFile("data/profiles/hd-bluray-web.json")
	require.NoError(t, err)
	seeds, err := quality.DecodeProfileSeeds(doc)
	require.NoError(t, err)
	p, errs := quality.FromCRD(&catalogv1alpha1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: "hd-bluray-web"}, Spec: seeds[0].Spec}, catalogue.LoadedCatalogue())
	require.Empty(t, errs)
	return p
}

// agentAnswer is the search agent's ranked answer for target over titles:
// pkg/decision's EvaluateRelease and the agent's RankScored.
func agentAnswer(t *testing.T, target decision.Target, titles ...string) []schema.ScoredRelease {
	t.Helper()
	rels := make([]commonv1.ReleaseInfo, 0, len(titles))
	for _, title := range titles {
		rels = append(rels, commonv1.ReleaseInfo{GUID: "guid-" + title, IndexerRef: "nzbgeek", Title: title, Protocol: commonv1.ProtocolTorrent})
	}
	o := decision.Options{UserInvoked: true, ProtocolsEnabled: map[string]bool{"torrent": true}}
	return worker.RankScored(decision.EvaluateRelease(context.Background(), target, hdProfile(t), catalogue.LoadedCatalogue(), rels, o), o, nil, 0)
}

func heatTarget() decision.Target {
	return decision.Target{
		Kind: commonv1.MediaKindMovie, Available: true, Monitored: true, OriginalLanguageTag: "en",
		Identity: decision.Identity{Titles: []string{"Heat"}, Year: 1995},
	}
}

// answer writes the agent's record answering s's task.
func (f *recFixture) answer(t *testing.T, s *catalogv1alpha1.Search, at time.Time, cands []schema.ScoredRelease) {
	t.Helper()
	rec := &schema.SearchRecord{
		RecordHeader: schema.RecordHeader{
			Item: &schema.ItemRef{Kind: "Search", Ref: schema.Ref{Namespace: s.Namespace, Name: s.Name, UID: string(s.UID)}},
			Seq:  max(1, s.Generation), State: records.StateAnswered,
		},
		Task: string(s.UID), FinishedAt: at,
		IndexerOutcomes: []schema.SearchOutcome{{IndexerRef: schema.Ref{Name: "nzbgeek"}, Status: schema.SearchOutcomeOK}},
		Candidates:      cands,
	}
	v, err := f.w.Write(context.Background(), agentrecords.SearchKey(string(s.UID)), rec)
	require.NoError(t, err)
	require.Equal(t, records.Wrote, v)
}

// seedStatus applies status (a map in the CRD's JSON form) to obj under
// manager: a test's stand-in for the item's own reconcilers.
func seedStatus(t *testing.T, c client.Client, obj client.Object, manager k8s.FieldManager, status map[string]any) {
	t.Helper()
	gvk, err := apiutil.GVKForObject(obj, c.Scheme())
	require.NoError(t, err)
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvk.GroupVersion().String(), "kind": gvk.Kind,
		"metadata": map[string]any{"name": obj.GetName(), "namespace": obj.GetNamespace()},
		"status":   status,
	}}
	_, err = k8s.PatchStatusUnstructured(context.Background(), c, manager, u)
	require.NoError(t, err)
}

func toMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(v)
	require.NoError(t, err)
	return m
}

// heat creates the movie Heat; with fileQuality set it has an imported file
// of that quality, transcoded when transcoded is.
func (f *recFixture) heat(t *testing.T, fileQuality string, transcoded bool) *catalogv1alpha1.Movie {
	t.Helper()
	ctx := context.Background()
	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: f.ns},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies"},
	}
	require.NoError(t, f.c.Create(ctx, m))
	status := map[string]any{"metadata": map[string]any{"title": "Heat", "year": int64(1995), "originalLanguage": "en"}}
	if fileQuality != "" {
		d, ok := quality.Lookup("video", fileQuality)
		require.True(t, ok)
		mf := &catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: "heat-file", Namespace: f.ns},
			Spec: catalogv1alpha1.MediaFileSpec{
				Path: "/data/movies/Heat (1995)/Heat.mkv", Quality: d.Quality,
				MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"},
			},
		}
		if transcoded {
			mf.Spec.Original = ptr.To(false)
		}
		require.NoError(t, f.c.Create(ctx, mf))
		status["hasFile"], status["fileRef"] = true, "heat-file"
	}
	seedStatus(t, f.c, m, k8s.ManagerCatalog, status)
	require.NoError(t, f.c.Get(ctx, client.ObjectKeyFromObject(m), m))
	return m
}

func titlesOf(rs []commonv1.ReleaseDecision) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Title)
	}
	return out
}

func hasReason(rd commonv1.ReleaseDecision, r decision.Reason) bool {
	for _, rej := range rd.Rejections {
		if r.Of(rej) {
			return true
		}
	}
	return false
}

// interleavingReader runs once after its first read of a Search: another
// writer landing between the compare-and-swap's read and its apply.
type interleavingReader struct {
	client.Reader
	once  sync.Once
	write func()
	reads int
}

func (r *interleavingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	err := r.Reader.Get(ctx, key, obj, opts...)
	if _, ok := obj.(*catalogv1alpha1.Search); ok && err == nil {
		r.reads++
		r.once.Do(r.write)
	}
	return err
}

// TestTheSearchControllerIncorporatesTheRecordByCAS is ADR-0019 §7.4 and
// W3's fence: the interactive answer is written into status by
// compare-and-swap, so a writer landing between the read and the apply is
// a Conflict redone from a fresh read -- never rolled back -- and an answer
// a newer one already superseded is not written over it.
func TestTheSearchControllerIncorporatesTheRecordByCAS(t *testing.T) {
	ctx := context.Background()

	t.Run("an interleaved write is kept and the answer still lands", func(t *testing.T) {
		f := newRecFixture(t, "search-cas-retry")
		f.heat(t, "", false)
		s := f.createSearch(t, "srch", catalogv1alpha1.SearchSpec{MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}, TTL: metav1.Duration{Duration: time.Hour}})
		f.reconcile(t, "srch")
		f.answer(t, f.get(t, "srch"), f.clock.Now(), agentAnswer(t, heatTarget(), "Heat.1995.1080p.BluRay.x264-GRP"))

		rd := &interleavingReader{Reader: f.c, write: func() {
			fresh := f.get(t, "srch")
			patch := client.MergeFrom(fresh.DeepCopy())
			fresh.Annotations = map[string]string{"test/interleaved": "yes"}
			require.NoError(t, f.c.Patch(ctx, fresh, patch))
		}}
		f.r.APIReader = rd
		f.reconcile(t, "srch")
		require.GreaterOrEqual(t, rd.reads, 2, "the first apply conflicted and the answer was rendered again from a fresh read")

		got := f.get(t, s.Name)
		require.Equal(t, "yes", got.Annotations["test/interleaved"])
		require.Equal(t, []string{"Heat.1995.1080p.BluRay.x264-GRP"}, titlesOf(got.Status.Results))
		require.NotNil(t, got.Status.FinishedAt)
		require.Len(t, got.Status.IndexerOutcomes, 1)
		require.Equal(t, catalogv1alpha1.SearchPhaseRunning, got.Status.Phase, "the controller's own fields are untouched")

		f.reconcile(t, "srch")
		require.Equal(t, catalogv1alpha1.SearchPhaseCompleted, f.get(t, "srch").Status.Phase)
	})

	t.Run("a newer answer landing first is not rolled back", func(t *testing.T) {
		f := newRecFixture(t, "search-cas-newer")
		f.heat(t, "", false)
		f.createSearch(t, "srch", catalogv1alpha1.SearchSpec{MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}, TTL: metav1.Duration{Duration: time.Hour}})
		f.reconcile(t, "srch")
		older := f.clock.Now()
		f.answer(t, f.get(t, "srch"), older, agentAnswer(t, heatTarget(), "Heat.1995.1080p.BluRay.x264-OLD"))

		newer := metav1.NewTime(older.Add(time.Minute))
		f.r.APIReader = &interleavingReader{Reader: f.c, write: func() {
			workerWrote(t, f.c, f.ns, "srch", newer, approvedResult("newer"))
		}}
		f.reconcile(t, "srch")

		got := f.get(t, "srch")
		require.Equal(t, newer.UTC(), got.Status.FinishedAt.UTC())
		require.Len(t, got.Status.Results, 1)
		require.Equal(t, "newer", got.Status.Results[0].GUID, "the stale answer did not overwrite the newer one")
	})
}

// TestInteractiveResultsStillShowItemStateRejections is ruling R24: the
// agent judges releases only; the Search controller adds the item-state
// rejections of the item's fresh state, so a person still sees a release
// the current file already beats, or one the release index blocks.
func TestInteractiveResultsStillShowItemStateRejections(t *testing.T) {
	const (
		bd720  = "Heat.1995.720p.BluRay.x264-GRP"
		bd1080 = "Heat.1995.1080p.BluRay.x264-GRP"
	)
	run := func(t *testing.T, ns, fileQuality string, block bool) map[string]commonv1.ReleaseDecision {
		f := newRecFixture(t, ns)
		f.heat(t, fileQuality, false)
		f.createSearch(t, "srch", catalogv1alpha1.SearchSpec{MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}, TTL: metav1.Duration{Duration: time.Hour}})
		f.reconcile(t, "srch")
		cands := agentAnswer(t, heatTarget(), bd720, bd1080)
		for i := range cands {
			require.True(t, cands[i].Decision.Approved, "the agent approves %s", cands[i].Decision.Title)
			if block && cands[i].Decision.Title == bd1080 {
				cands[i].Blocked = &schema.ReleaseBlock{Scope: schema.BlockScopeGlobal, Reason: "failed"}
			}
		}
		f.answer(t, f.get(t, "srch"), f.clock.Now(), cands)
		f.reconcile(t, "srch")
		out := map[string]commonv1.ReleaseDecision{}
		for _, r := range f.get(t, "srch").Status.Results {
			out[r.Title] = r
		}
		return out
	}

	t.Run("no file: both approved", func(t *testing.T) {
		res := run(t, "search-r24-none", "", false)
		require.True(t, res[bd720].Approved)
		require.True(t, res[bd1080].Approved)
	})
	t.Run("a file at the cutoff", func(t *testing.T) {
		res := run(t, "search-r24-file", "Bluray-1080p", false)
		require.False(t, res[bd720].Approved)
		require.True(t, hasReason(res[bd720], decision.ReasonExistingHigherPreference), "%+v", res[bd720].Rejections)
		require.False(t, res[bd1080].Approved)
		require.Equal(t, []string{bd1080, bd720}, titlesOf(f2results(res)), "rejected releases keep the agent's order")
	})
	t.Run("a blocked release", func(t *testing.T) {
		res := run(t, "search-r24-block", "", true)
		require.True(t, res[bd720].Approved)
		require.False(t, res[bd1080].Approved)
		require.True(t, hasReason(res[bd1080], decision.ReasonBlocklisted), "%+v", res[bd1080].Rejections)
	})
}

// f2results orders a result map back by rank.
func f2results(m map[string]commonv1.ReleaseDecision) []commonv1.ReleaseDecision {
	out := make([]commonv1.ReleaseDecision, len(m))
	for _, r := range m {
		out[r.Rank-1] = r
	}
	return out
}

// TestTranscodedFinalBindsGrabBestButNotAPick is main's 22230298 rule,
// kept across A4.3: "Search now" (grabBest) never grabs over a transcoded
// file, since the controller's results carry TranscodedFinal; a person's
// own pick of the same release is still sent, as a manual candidate.
func TestTranscodedFinalBindsGrabBestButNotAPick(t *testing.T) {
	ctx := context.Background()
	f := newRecFixture(t, "search-transcoded")
	f.heat(t, "Bluray-720p", true)
	f.createSearch(t, "srch", catalogv1alpha1.SearchSpec{
		MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}, GrabBest: true, TTL: metav1.Duration{Duration: time.Hour},
	})
	f.reconcile(t, "srch")
	cands := agentAnswer(t, heatTarget(), "Heat.1995.1080p.BluRay.x264-GRP")
	require.True(t, cands[0].Decision.Approved, "an upgrade the agent approves")
	f.answer(t, f.get(t, "srch"), f.clock.Now(), cands)
	f.reconcile(t, "srch") // incorporate
	f.reconcile(t, "srch") // complete
	f.reconcile(t, "srch") // grabBest

	got := f.get(t, "srch")
	require.Equal(t, catalogv1alpha1.SearchPhaseCompleted, got.Status.Phase)
	require.True(t, hasReason(got.Status.Results[0], decision.ReasonTranscodedFinal), "%+v", got.Status.Results[0].Rejections)
	require.Empty(t, f.candidates(t), "Search now grabs nothing over a transcoded file")

	guid := got.Status.Results[0].GUID
	patch := client.MergeFrom(got.DeepCopy())
	got.Spec.Grab = []string{guid}
	require.NoError(t, f.c.Patch(ctx, got, patch))
	f.reconcile(t, "srch")

	sent := f.candidates(t)
	require.Len(t, sent, 1, "a person's pick is sent: %+v", f.get(t, "srch").Status.Grabbed)
	require.True(t, sent[0].Manual)
	require.False(t, sent[0].Override, "no spec.override was needed")
	require.Equal(t, guid, sent[0].Release.Decision.GUID)
}

// TestAPickBecomesAManualCandidateForTheOwner is §6.6 step 4: a person's
// pick from an episode's Search is sent to the episode's grab owner, the
// Series, as a manual interactive candidate naming the episode, on the
// owner's intake subject under the pick's Msg-Id.
func TestAPickBecomesAManualCandidateForTheOwner(t *testing.T) {
	ctx := context.Background()
	f := newRecFixture(t, "search-pick-owner")
	ser := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "breaking-bad", Namespace: f.ns},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 81189, QualityProfileRef: "hd-bluray-web", RootFolderRef: "tv"},
	}
	require.NoError(t, f.c.Create(ctx, ser))
	epi := &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Name: "breaking-bad-s01e05", Namespace: f.ns},
		Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "breaking-bad", SeasonNumber: 1, EpisodeNumber: 5},
	}
	require.NoError(t, f.c.Create(ctx, epi))

	s := f.createSearch(t, "srch", catalogv1alpha1.SearchSpec{
		MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "breaking-bad-s01e05"}, TTL: metav1.Duration{Duration: time.Hour},
	})
	f.reconcile(t, "srch")
	cands := agentAnswer(t, decision.Target{
		Kind: commonv1.MediaKindEpisode, Available: true,
		Identity: decision.Identity{Titles: []string{"Breaking Bad"}, Season: 1, Episodes: []int{5}, SingleEpisodeSearch: true},
	}, "Breaking.Bad.S01E05.1080p.BluRay.x264-GRP")
	f.answer(t, f.get(t, "srch"), f.clock.Now(), cands)
	f.reconcile(t, "srch")
	f.reconcile(t, "srch")
	got := f.get(t, "srch")
	require.Equal(t, catalogv1alpha1.SearchPhaseCompleted, got.Status.Phase)

	patch := client.MergeFrom(got.DeepCopy())
	got.Spec.Grab = []string{cands[0].Decision.GUID}
	require.NoError(t, f.c.Patch(ctx, got, patch))
	f.reconcile(t, "srch")

	sent := f.candidates(t)
	require.Len(t, sent, 1)
	c := sent[0]
	require.Equal(t, "Series", c.Owner.Kind)
	require.Equal(t, "breaking-bad", c.Owner.Name)
	require.Equal(t, string(f.getSeries(t).UID), c.Owner.UID)
	require.Len(t, c.Targets, 1)
	require.Equal(t, "Episode", c.Targets[0].Kind)
	require.Equal(t, "breaking-bad-s01e05", c.Targets[0].Name)
	require.True(t, c.Manual)
	require.Equal(t, schema.CandidateOriginInteractive, c.Origin)
	require.Equal(t, commonv1.GrabSourceInteractive, c.GrabbedBy)
	require.Equal(t, string(s.UID), c.SearchRef.UID)
	require.Nil(t, c.Season)
	require.Equal(t, cands[0].Parsed.Coverage, c.Release.Parsed.Coverage, "the agent's scored release, coverage included")

	subjects, envs := f.pub.snapshot()
	last := len(subjects) - 1
	require.Equal(t, events.IntakeCandidateSubject("Series", c.Owner.UID), subjects[last])
	require.Equal(t, events.MsgIDForCandidate(events.CandidateMsgOriginPick, string(s.UID), "nzbgeek", cands[0].Decision.GUID), envs[last].ID)
}

func (f *recFixture) getSeries(t *testing.T) *catalogv1alpha1.Series {
	t.Helper()
	var s catalogv1alpha1.Series
	require.NoError(t, f.c.Get(context.Background(), client.ObjectKey{Namespace: f.ns, Name: "breaking-bad"}, &s))
	return &s
}

// TestIncorporateWritesOtherTitlesWithResults is design §8.2: an answer is
// banded -- approved, rejected, other titles -- and status.otherTitles,
// the trailing band's size, is written in the same apply as results, both
// owned by catalog-worker.
func TestIncorporateWritesOtherTitlesWithResults(t *testing.T) {
	f := newRecFixture(t, "search-other-titles")
	f.heat(t, "", false)
	f.createSearch(t, "srch", catalogv1alpha1.SearchSpec{MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}, TTL: metav1.Duration{Duration: time.Hour}})
	f.reconcile(t, "srch")
	cands := agentAnswer(t, heatTarget(),
		"Heat.1995.1080p.BluRay.x264-GRP", "Heat.1995.480p.DVDRip.x264-GRP",
		"Heat.2023.1080p.WEB-DL.x264-GRP", "The.Insider.1999.1080p.BluRay.x264-GRP")
	f.answer(t, f.get(t, "srch"), f.clock.Now(), cands)
	f.reconcile(t, "srch")

	got := f.get(t, "srch")
	require.EqualValues(t, 2, got.Status.OtherTitles)
	var bands []string
	for _, r := range got.Status.Results {
		bands = append(bands, decision.BandOf(r.Approved, r.Rejections).String())
	}
	require.Equal(t, []string{"approved", "rejected", "otherTitles", "otherTitles"}, bands)

	owned := statusFieldsOwnedBy(t, got, string(k8s.ManagerCatalogWorker))
	require.Contains(t, owned, "f:results")
	require.Contains(t, owned, "f:otherTitles")
	require.Contains(t, owned, "f:finishedAt")
	require.NotContains(t, statusFieldsOwnedBy(t, got, string(k8s.ManagerCatalog)), "f:otherTitles")
}

// TestASeasonPickReachesTheSeriesWithItsSeason is design §8.3: an
// interactive season search asks the agent for its season, with the Series
// as the item and no container, and its pick goes to the Series with the
// season and no Targets -- the Series resolves what it covers.
func TestASeasonPickReachesTheSeriesWithItsSeason(t *testing.T) {
	ctx := context.Background()
	f := newRecFixture(t, "search-season-pick")
	require.NoError(t, f.c.Create(ctx, &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "breaking-bad", Namespace: f.ns},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 81189, QualityProfileRef: "hd-bluray-web", RootFolderRef: "tv"},
	}))
	season := int32(1)
	s := f.createSearch(t, "srch", catalogv1alpha1.SearchSpec{
		MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: "breaking-bad"}, Season: &season, TTL: metav1.Duration{Duration: time.Hour},
	})
	f.reconcile(t, "srch")
	subjects, envs := f.pub.snapshot()
	require.Len(t, subjects, 1)
	var task schema.SearchTask
	require.NoError(t, schema.Decode(envs[0].Schema, envs[0].Data, &task))
	require.Equal(t, int32(1), *task.Season)
	require.Equal(t, "Series", task.Item.Kind)
	require.Nil(t, task.Container)

	cands := agentAnswer(t, decision.Target{
		Kind: commonv1.MediaKindSeries, Available: true,
		Identity: decision.Identity{Titles: []string{"Breaking Bad"}, Season: 1, Episodes: []int{1, 2, 3, 4, 5, 6, 7}, SeasonSearch: true},
	}, "Breaking.Bad.S01.1080p.BluRay.x264-GRP")
	f.answer(t, f.get(t, "srch"), f.clock.Now(), cands)
	f.reconcile(t, "srch")
	f.reconcile(t, "srch")
	got := f.get(t, "srch")
	patch := client.MergeFrom(got.DeepCopy())
	got.Spec.Grab = []string{cands[0].Decision.GUID}
	require.NoError(t, f.c.Patch(ctx, got, patch))
	f.reconcile(t, "srch")

	sent := f.candidates(t)
	require.Len(t, sent, 1)
	require.Equal(t, "Series", sent[0].Owner.Kind)
	require.NotNil(t, sent[0].Season)
	require.Equal(t, int32(1), *sent[0].Season)
	require.Empty(t, sent[0].Targets)
	require.Equal(t, string(s.UID), sent[0].SearchRef.UID)
	require.NotNil(t, sent[0].Release.Parsed.Coverage, "the Series resolves the pick from the agent's coverage")
}

// drainEvents is every Event the fake recorder holds, as "Type Reason
// note".
func drainEvents(f *fixture) []string {
	rec := f.r.Recorder.(*k8sevents.FakeRecorder)
	var out []string
	for {
		select {
		case e := <-rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func countPrefix(es []string, prefix string) int {
	n := 0
	for _, e := range es {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}

// seriesState renders a Series' status.seasonSearch serving req.
func seriesState(req *catalogv1alpha1.Search, started time.Time, seasons []catalogv1alpha1.SeasonSearchSeason, finished *time.Time, dispatchedAt *time.Time) map[string]any {
	st := catalogv1alpha1.SeasonSearchState{
		Request:   catalogv1alpha1.SeasonSearchRequest{Name: req.Name, UID: string(req.UID)},
		StartedAt: metav1.NewTime(started), Seasons: seasons,
	}
	if finished != nil {
		at := metav1.NewTime(*finished)
		st.FinishedAt = &at
	}
	if dispatchedAt != nil {
		st.Dispatch = &catalogv1alpha1.Dispatch{Seq: 7, AnsweredSeq: 6, DispatchedAt: metav1.NewTime(*dispatchedAt)}
	}
	return map[string]any{"seasonSearch": st}
}

func (f *recFixture) series(t *testing.T) *catalogv1alpha1.Series {
	t.Helper()
	s := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "breaking-bad", Namespace: f.ns},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 81189, QualityProfileRef: "hd-bluray-web", RootFolderRef: "tv"},
	}
	require.NoError(t, f.c.Create(context.Background(), s))
	return s
}

func (f *recFixture) plannedSearch(t *testing.T, name string) *catalogv1alpha1.Search {
	t.Helper()
	return f.createSearch(t, name, catalogv1alpha1.SearchSpec{
		MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: "breaking-bad"}, GrabBest: true, TTL: metav1.Duration{Duration: time.Hour},
	})
}

func readyReason(s *catalogv1alpha1.Search) string {
	if c := k8s.FindCondition(s.Status.Conditions, k8s.ConditionReady); c != nil {
		return c.Reason
	}
	return ""
}

// TestAPlannedSearchMirrorsTheSeries is design §4.6: a planned Search
// mirrors its Series' season search -- the seasons, their phases and
// counts, the grabs made of each answer -- reads Completed with the state's
// finishedAt once the Series finishes, and expires on its TTL from then.
func TestAPlannedSearchMirrorsTheSeries(t *testing.T) {
	ctx := context.Background()
	f := newRecFixture(t, "search-planned-mirror")
	ser := f.series(t)
	s := f.plannedSearch(t, "srch")
	now := f.clock.Now()

	// The Series took the request: season 1 searching, season 2 skipped.
	searching := []catalogv1alpha1.SeasonSearchSeason{
		{Number: 1, Phase: catalogv1alpha1.SeasonSearchSearching, Seq: 7, Attempts: 1},
		{Number: 2, Phase: catalogv1alpha1.SeasonSearchSkipped, Reason: catalogv1alpha1.SeasonSearchNothingWanted},
	}
	seedStatus(t, f.c, ser, k8s.ManagerCatalogGrab, seriesState(s, now, searching, nil, &now))
	f.reconcile(t, "srch")
	got := f.get(t, "srch")
	require.Equal(t, catalogv1alpha1.SearchPhaseRunning, got.Status.Phase)
	require.Equal(t, "Searching", readyReason(got))
	require.Len(t, got.Status.Seasons, 2)
	require.Equal(t, catalogv1alpha1.SeasonSearchSearching, got.Status.Seasons[0].Phase)
	require.Equal(t, catalogv1alpha1.SeasonSearchNothingWanted, got.Status.Seasons[1].Reason)
	require.Equal(t, 1, countPrefix(drainEvents(f.fixture), "Normal SeriesSearchStarted"))

	// The season's answer: two approved releases; the Series grabbed one
	// of them after the answer (and holds an older grab of the other).
	answeredAt := now.Add(time.Minute)
	pack := agentAnswer(t, decision.Target{
		Kind: commonv1.MediaKindSeries, Available: true,
		Identity: decision.Identity{Titles: []string{"Breaking Bad"}, Season: 1, Episodes: []int{1, 2, 3}, SeasonSearch: true},
	}, "Breaking.Bad.S01.1080p.BluRay.x264-GRP", "Breaking.Bad.S01E01.1080p.BluRay.x264-GRP")
	season := int32(1)
	_, err := f.w.Write(ctx, agentrecords.SeasonSearchKey(string(ser.UID), 1), &schema.SearchRecord{
		RecordHeader: schema.RecordHeader{
			Item: &schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: f.ns, Name: ser.Name, UID: string(ser.UID)}},
			Sub:  schema.SeasonSub(1), Seq: 7, State: records.StateAnswered,
		},
		Task: string(ser.UID), FinishedAt: answeredAt, Candidates: pack, Season: &season,
	})
	require.NoError(t, err)
	grab := func(guid string, at time.Time) map[string]any {
		e := catalogv1alpha1.DownloadEntry{
			ID: "bb-" + guid[len(guid)-8:], UID: "uid-" + guid[len(guid)-8:],
			Release: catalogv1alpha1.DownloadRelease{Title: "x", GUID: guid, IndexerRef: "nzbgeek", Protocol: commonv1.ProtocolTorrent},
			Source:  commonv1.DownloadSource{IndexerDownload: &commonv1.IndexerDownload{IndexerRef: "nzbgeek", GUID: guid}},
			Client:  "torrent", GrabbedBy: commonv1.GrabSourceSearch, GrabbedAt: metav1.NewTime(at),
			Phase: commonv1.DownloadPhasePending, RemoveOnImport: true, RemoveDataOnDelete: true,
			Dispatch: catalogv1alpha1.Dispatch{Seq: 1, DispatchedAt: metav1.NewTime(at)},
		}
		return toMap(t, &e)
	}
	ans := metav1.NewTime(answeredAt.Truncate(time.Second))
	answered := []catalogv1alpha1.SeasonSearchSeason{
		{Number: 1, Phase: catalogv1alpha1.SeasonSearchAnswered, Seq: 7, Attempts: 1, AnsweredAt: &ans, Candidates: 2, Approved: 2},
		searching[1],
	}
	finished := answeredAt.Add(time.Minute)
	status := seriesState(s, now, answered, &finished, &now)
	status["downloads"] = []any{grab(pack[0].Decision.GUID, answeredAt.Add(30*time.Second)), grab(pack[1].Decision.GUID, now.Add(-time.Hour))}
	seedStatus(t, f.c, ser, k8s.ManagerCatalogGrab, status)

	f.reconcile(t, "srch")
	got = f.get(t, "srch")
	require.Equal(t, catalogv1alpha1.SearchPhaseCompleted, got.Status.Phase)
	require.Equal(t, finished.Truncate(time.Second), got.Status.FinishedAt.UTC())
	require.EqualValues(t, 2, got.Status.Seasons[0].Results)
	require.EqualValues(t, 2, got.Status.Seasons[0].Approved)
	require.EqualValues(t, 1, got.Status.Seasons[0].Grabbed, "only the grab made of the answer counts")
	require.True(t, k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionReady))
	require.Equal(t, 1, countPrefix(drainEvents(f.fixture), "Normal SeriesSearchFinished"))
	require.NotNil(t, got.Status.StartedAt)

	f.clock.Advance(time.Hour + 3*time.Minute)
	f.reconcile(t, "srch")
	err = f.c.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "srch"}, &catalogv1alpha1.Search{})
	require.True(t, apierrors.IsNotFound(err), "the TTL runs from finishedAt: %v", err)
}

// TestAQueuedPlannedSearchWaitsAndThenRuns is design §4.6: while the
// Series serves another request the Search reads Ready False Queued (one
// Event), and once the Series takes it, Searching.
func TestAQueuedPlannedSearchWaitsAndThenRuns(t *testing.T) {
	f := newRecFixture(t, "search-planned-queued")
	ser := f.series(t)
	first := f.plannedSearch(t, "first")
	second := f.plannedSearch(t, "second")
	now := f.clock.Now()
	seedStatus(t, f.c, ser, k8s.ManagerCatalogGrab, seriesState(first, now,
		[]catalogv1alpha1.SeasonSearchSeason{{Number: 1, Phase: catalogv1alpha1.SeasonSearchSearching, Seq: 7}}, nil, &now))

	f.reconcile(t, "second")
	f.reconcile(t, "second")
	got := f.get(t, "second")
	require.Equal(t, catalogv1alpha1.SearchPhaseRunning, got.Status.Phase)
	require.Equal(t, "Queued", readyReason(got))
	require.Contains(t, k8s.FindCondition(got.Status.Conditions, k8s.ConditionReady).Message, "first")
	require.Empty(t, got.Status.Seasons)
	require.Equal(t, 1, countPrefix(drainEvents(f.fixture), "Normal SeriesSearchQueued"), "one Event, not one per pass")

	f.clock.Advance(time.Minute)
	seedStatus(t, f.c, ser, k8s.ManagerCatalogGrab, seriesState(second, f.clock.Now(),
		[]catalogv1alpha1.SeasonSearchSeason{{Number: 1, Phase: catalogv1alpha1.SeasonSearchQueued}}, nil, nil))
	f.reconcile(t, "second")
	got = f.get(t, "second")
	require.Equal(t, "Searching", readyReason(got))
	require.Len(t, got.Status.Seasons, 1)
}

// TestAPlannedSearchFailsWhenStalled is design §4.6: a Search the Series
// never takes, or whose served state shows no transition, for
// SeriesSearchStallTimeout fails Stalled with a Warning; the Series is left
// alone.
func TestAPlannedSearchFailsWhenStalled(t *testing.T) {
	t.Run("never taken", func(t *testing.T) {
		f := newRecFixture(t, "search-planned-untaken")
		f.series(t)
		f.plannedSearch(t, "srch")
		f.reconcile(t, "srch")
		require.Equal(t, "Queued", readyReason(f.get(t, "srch")))

		f.clock.Advance(31 * time.Minute)
		f.reconcile(t, "srch")
		got := f.get(t, "srch")
		require.Equal(t, catalogv1alpha1.SearchPhaseFailed, got.Status.Phase)
		cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.SearchConditionFailed)
		require.NotNil(t, cond)
		require.Equal(t, "Stalled", cond.Reason)
		require.Equal(t, 1, countPrefix(drainEvents(f.fixture), "Warning SeriesSearchStalled"))
	})
	t.Run("taken and stuck", func(t *testing.T) {
		f := newRecFixture(t, "search-planned-stuck")
		ser := f.series(t)
		s := f.plannedSearch(t, "srch")
		now := f.clock.Now()
		seedStatus(t, f.c, ser, k8s.ManagerCatalogGrab, seriesState(s, now,
			[]catalogv1alpha1.SeasonSearchSeason{{Number: 1, Phase: catalogv1alpha1.SeasonSearchSearching, Seq: 7}}, nil, &now))
		f.reconcile(t, "srch")
		f.clock.Advance(29 * time.Minute)
		f.reconcile(t, "srch")
		require.Equal(t, catalogv1alpha1.SearchPhaseRunning, f.get(t, "srch").Status.Phase, "inside the timeout")
		f.clock.Advance(2 * time.Minute)
		f.reconcile(t, "srch")
		require.Equal(t, catalogv1alpha1.SearchPhaseFailed, f.get(t, "srch").Status.Phase)
		require.NotNil(t, f.getSeries(t).Status.SeasonSearch, "the Series' state is its own")
	})
	t.Run("series gone", func(t *testing.T) {
		f := newRecFixture(t, "search-planned-gone")
		f.plannedSearch(t, "srch")
		f.reconcile(t, "srch")
		cond := k8s.FindCondition(f.get(t, "srch").Status.Conditions, catalogv1alpha1.SearchConditionFailed)
		require.NotNil(t, cond)
		require.Equal(t, "SeriesGone", cond.Reason)
	})
}

// TestAPlannedSearchNeverPicks is design §4.6: the Series grabs what its
// seasons find; the planned Search publishes no task, sends no pick and
// writes no results, even with a record at its own key.
func TestAPlannedSearchNeverPicks(t *testing.T) {
	f := newRecFixture(t, "search-planned-nopick")
	ser := f.series(t)
	s := f.plannedSearch(t, "srch")
	f.answer(t, s, f.clock.Now(), agentAnswer(t, decision.Target{
		Kind: commonv1.MediaKindSeries, Available: true,
		Identity: decision.Identity{Titles: []string{"Breaking Bad"}, Season: 1, Episodes: []int{1, 2}, SeasonSearch: true},
	}, "Breaking.Bad.S01.1080p.BluRay.x264-GRP"))
	now := f.clock.Now()
	// While the Series searches, the Search is Running with no finishedAt:
	// the window an interactive Search incorporates its record in.
	seedStatus(t, f.c, ser, k8s.ManagerCatalogGrab, seriesState(s, now,
		[]catalogv1alpha1.SeasonSearchSeason{{Number: 1, Phase: catalogv1alpha1.SeasonSearchSearching, Seq: 7}}, nil, &now))
	for range 3 {
		f.reconcile(t, "srch")
	}
	running := f.get(t, "srch")
	require.Equal(t, catalogv1alpha1.SearchPhaseRunning, running.Status.Phase)
	require.Nil(t, running.Status.FinishedAt)
	require.Empty(t, running.Status.Results, "a planned Search incorporates no record")

	ans := metav1.NewTime(now.Truncate(time.Second))
	seedStatus(t, f.c, ser, k8s.ManagerCatalogGrab, seriesState(s, now,
		[]catalogv1alpha1.SeasonSearchSeason{{Number: 1, Phase: catalogv1alpha1.SeasonSearchAnswered, Seq: 7, AnsweredAt: &ans, Candidates: 1, Approved: 1}}, &now, &now))
	for range 3 {
		f.reconcile(t, "srch")
	}
	got := f.get(t, "srch")
	require.Equal(t, catalogv1alpha1.SearchPhaseCompleted, got.Status.Phase)
	require.Empty(t, got.Status.Results)
	require.Empty(t, got.Status.Grabbed)
	subjects, _ := f.pub.snapshot()
	require.Empty(t, subjects, "no search task and no pick")
}
