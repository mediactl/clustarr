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
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/wantedcron"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/version"
)

// wantedScanEnvelope builds what wantedcron actually publishes, so the test
// exercises the same bytes and the same Clustarr-Schema header the sweep does.
func wantedScanEnvelope(t *testing.T, scan schema.WantedScan) *events.Envelope {
	t.Helper()
	schemaName, data, err := schema.Encode(scan)
	require.NoError(t, err)
	return &events.Envelope{
		ID:     "wantedscan-" + scan.Namespace,
		Type:   "catalog.WantedScan",
		Schema: schemaName,
		Source: "catalogarr@" + version.String(),
		Key:    scan.Namespace + "/",
		Time:   time.Now().UTC(),
		Data:   data,
	}
}

func createMovie(t *testing.T, ctx context.Context, c client.Client, ns, name, profile string) *catalogv1alpha1.Movie {
	t.Helper()
	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID: 603, QualityProfileRef: profile, RootFolderRef: "movies",
		},
	}
	require.NoError(t, c.Create(ctx, m))
	return m
}

// TestWorkerExpandsAWantedScanInsteadOfDiscardingIt is the regression test for
// the consumer-filter mismatch: catalogarr-search-normal filters
// clustarr.work.catalogarr.wantedscan.> as well as the search subjects, so the
// worker is handed catalog.WantedScan.v1 and decoding everything as a
// SearchTask dead-lettered every twelve-hourly sweep on first delivery.
func TestWorkerExpandsAWantedScanInsteadOfDiscardingIt(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-wantedscan")
	pub := &recordingPublisher{}
	f.worker.Publisher = pub

	// the-matrix already exists (fixture) with no status -> phase "" -> not wanted.
	profile := "wf-worker-wantedscan"
	wanted := createMovie(t, ctx, f.mgr, f.ns, "wanted-movie", profile)
	cutoff := createMovie(t, ctx, f.mgr, f.ns, "cutoff-movie", profile)
	backedOff := createMovie(t, ctx, f.mgr, f.ns, "backed-off-movie", profile)
	createMovie(t, ctx, f.mgr, f.ns, "downloading-movie", profile)

	setMoviePhase(t, ctx, f.mgr, f.ns, "wanted-movie", catalogv1alpha1.MoviePhaseWanted, nil)
	setMoviePhase(t, ctx, f.mgr, f.ns, "cutoff-movie", catalogv1alpha1.MoviePhaseCutoffUnmet, nil)
	setMoviePhase(t, ctx, f.mgr, f.ns, "downloading-movie", catalogv1alpha1.MoviePhaseDownloading, nil)
	// Searched a minute ago: inside wantedcron's six-hour floor.
	justNow := metav1.NewTime(time.Now().Add(-time.Minute))
	setMoviePhase(t, ctx, f.mgr, f.ns, "backed-off-movie", catalogv1alpha1.MoviePhaseWanted, &justNow)

	for _, name := range []string{"wanted-movie", "cutoff-movie", "backed-off-movie", "downloading-movie"} {
		n := name
		eventually(t, 10*time.Second, "the cache to see "+n+"'s phase", func() bool {
			var m catalogv1alpha1.Movie
			if err := f.mgr.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: n}, &m); err != nil {
				return false
			}
			return m.Status.Phase != ""
		})
	}

	env := wantedScanEnvelope(t, schema.WantedScan{
		Namespace: f.ns, CutoffUnmet: true, Epoch: 1700000000,
	})
	require.NoError(t, f.worker.Handle(ctx, testMessage{env: env}),
		"a WantedScan must be expanded, not dead-lettered")

	subjects, envs := pub.snapshot()
	require.Len(t, subjects, 2, "only the two eligible movies are searched")

	var names []string
	for _, e := range envs {
		var task schema.SearchTask
		require.NoError(t, json.Unmarshal(e.Data, &task))
		names = append(names, task.MediaRef.Name)
		require.Equal(t, commonv1.MediaKindMovie, task.MediaRef.Kind)
		require.False(t, task.UserInvoked, "a cron sweep is not user invoked")
		require.Nil(t, task.SearchRef, "a swept item has no Search object to write to")
		require.Equal(t, f.ns+"/"+task.MediaRef.Name, e.Key)
	}
	sort.Strings(names)
	require.Equal(t, []string{"cutoff-movie", "wanted-movie"}, names)
	require.NotContains(t, names, "downloading-movie", "a Downloading item is already handled")
	require.NotContains(t, names, "backed-off-movie", "wantedcron.Eligible holds it inside the six-hour floor")

	sort.Strings(subjects)
	wantSubjects := []string{
		events.WorkSearchSubject(events.PriorityNormal,
			events.MediaKey(string(commonv1.MediaKindMovie), f.ns, "cutoff-movie")),
		events.WorkSearchSubject(events.PriorityNormal,
			events.MediaKey(string(commonv1.MediaKindMovie), f.ns, "wanted-movie")),
	}
	sort.Strings(wantSubjects)
	require.Equal(t, wantSubjects, subjects, "the sweep fans out at the normal tier, keyed by mediaKey")

	// Reasons are per item, not per sweep.
	reasons := map[string]schema.SearchReason{}
	for _, e := range envs {
		var task schema.SearchTask
		require.NoError(t, json.Unmarshal(e.Data, &task))
		reasons[task.MediaRef.Name] = task.Reason
	}
	require.Equal(t, schema.SearchReasonMissing, reasons["wanted-movie"])
	require.Equal(t, schema.SearchReasonCutoffUnmet, reasons["cutoff-movie"])

	// The message id is derived from the sweep's epoch, so a redelivery
	// deduplicates inside the stream's window instead of searching twice.
	require.Equal(t, events.MsgIDForObject(string(wanted.UID), 1700000000, "search"),
		msgIDFor(envs, "wanted-movie", t))
	require.Equal(t, events.MsgIDForObject(string(cutoff.UID), 1700000000, "search"),
		msgIDFor(envs, "cutoff-movie", t))
	_ = backedOff
}

func msgIDFor(envs []*events.Envelope, name string, t *testing.T) string {
	t.Helper()
	for _, e := range envs {
		var task schema.SearchTask
		require.NoError(t, json.Unmarshal(e.Data, &task))
		if task.MediaRef.Name == name {
			return e.ID
		}
	}
	t.Fatalf("no envelope for %s", name)
	return ""
}

func setMoviePhase(t *testing.T, ctx context.Context, c client.Client, ns, name string, phase catalogv1alpha1.MoviePhase, lastSearchedAt *metav1.Time) {
	t.Helper()
	status := catalogac.MovieStatus().WithPhase(phase)
	if lastSearchedAt != nil {
		status = status.WithLastSearchedAt(*lastSearchedAt)
	}
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalog, catalogac.Movie(name, ns).WithStatus(status))
	require.NoError(t, err)
}

// TestWorkerWantedScanSkipsCutoffUnmetUnlessAsked pins WantedScan.CutoffUnmet:
// a sweep that did not ask for upgrades must not enqueue them.
func TestWorkerWantedScanSkipsCutoffUnmetUnlessAsked(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-wantedscan-missing-only")
	pub := &recordingPublisher{}
	f.worker.Publisher = pub

	profile := "wf-worker-wantedscan-missing-only"
	createMovie(t, ctx, f.mgr, f.ns, "wanted-movie", profile)
	createMovie(t, ctx, f.mgr, f.ns, "cutoff-movie", profile)
	setMoviePhase(t, ctx, f.mgr, f.ns, "wanted-movie", catalogv1alpha1.MoviePhaseWanted, nil)
	setMoviePhase(t, ctx, f.mgr, f.ns, "cutoff-movie", catalogv1alpha1.MoviePhaseCutoffUnmet, nil)
	eventually(t, 10*time.Second, "both phases to reach the cache", func() bool {
		var list catalogv1alpha1.MovieList
		if err := f.mgr.List(ctx, &list, client.InNamespace(f.ns)); err != nil {
			return false
		}
		n := 0
		for i := range list.Items {
			if list.Items[i].Status.Phase != "" {
				n++
			}
		}
		return n == 2
	})

	require.NoError(t, f.worker.Handle(ctx, testMessage{env: wantedScanEnvelope(t, schema.WantedScan{
		Namespace: f.ns, CutoffUnmet: false, Epoch: 42,
	})}))

	_, envs := pub.snapshot()
	require.Len(t, envs, 1)
	var task schema.SearchTask
	require.NoError(t, json.Unmarshal(envs[0].Data, &task))
	require.Equal(t, "wanted-movie", task.MediaRef.Name)
}

// TestWorkerWantedScanKindsFilter pins WantedScan.Kinds.
func TestWorkerWantedScanKindsFilter(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-wantedscan-kinds")
	pub := &recordingPublisher{}
	f.worker.Publisher = pub

	createMovie(t, ctx, f.mgr, f.ns, "wanted-movie", "wf-worker-wantedscan-kinds")
	setMoviePhase(t, ctx, f.mgr, f.ns, "wanted-movie", catalogv1alpha1.MoviePhaseWanted, nil)
	eventually(t, 10*time.Second, "the phase to reach the cache", func() bool {
		var m catalogv1alpha1.Movie
		if err := f.mgr.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "wanted-movie"}, &m); err != nil {
			return false
		}
		return m.Status.Phase != ""
	})

	require.NoError(t, f.worker.Handle(ctx, testMessage{env: wantedScanEnvelope(t, schema.WantedScan{
		Namespace: f.ns, Kinds: []commonv1.MediaKind{commonv1.MediaKindEpisode}, Epoch: 7,
	})}))
	_, envs := pub.snapshot()
	require.Empty(t, envs, "an episodes-only sweep must not enqueue a movie")
}

// TestWorkerWantedScanRetriesOnBackPressure: a full work queue naks the whole
// sweep rather than dropping the rest of the namespace.
func TestWorkerWantedScanRetriesOnBackPressure(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-wantedscan-full")
	pub := &recordingPublisher{err: events.ErrQueueFull}
	f.worker.Publisher = pub

	createMovie(t, ctx, f.mgr, f.ns, "wanted-movie", "wf-worker-wantedscan-full")
	setMoviePhase(t, ctx, f.mgr, f.ns, "wanted-movie", catalogv1alpha1.MoviePhaseWanted, nil)
	eventually(t, 10*time.Second, "the phase to reach the cache", func() bool {
		var m catalogv1alpha1.Movie
		if err := f.mgr.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "wanted-movie"}, &m); err != nil {
			return false
		}
		return m.Status.Phase != ""
	})

	err := f.worker.Handle(ctx, testMessage{env: wantedScanEnvelope(t, schema.WantedScan{
		Namespace: f.ns, Epoch: 9,
	})})
	var re *events.RetryError
	require.ErrorAs(t, err, &re, "back-pressure naks the sweep; it must not be dead-lettered")
}

// TestWorkerWantedScanBackoffMatchesWantedcron guards the one piece of policy
// this package shares with app/catalog/controller/wantedcron: if the ladder
// there changes, the search worker follows, because it calls the same
// exported function rather than restating it.
func TestWorkerWantedScanBackoffMatchesWantedcron(t *testing.T) {
	now := time.Now()
	latest := metav1.NewTime(now.Add(-wantedcron.MinimumGap / 2))
	require.False(t, wantedcron.Eligible(commonv1.Attempts{Count: 1, Latest: &latest}, now))

	older := metav1.NewTime(now.Add(-wantedcron.MinimumGap - time.Minute))
	require.True(t, wantedcron.Eligible(commonv1.Attempts{Count: 1, Latest: &older}, now))
	require.True(t, wantedcron.Eligible(commonv1.Attempts{}, now), "a never-searched item is never held back")
}

// TestWorkerDiscardsAnUnknownSchema: the dispatch must not silently treat a
// third payload as a SearchTask the way the original code did.
func TestWorkerDiscardsAnUnknownSchema(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-unknown-schema")

	env := f.envelope(t, schema.SearchTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
	})
	env.Schema = "catalog.SomethingElse.v1"

	err := f.worker.Handle(ctx, testMessage{env: env})
	var de *events.DiscardError
	require.ErrorAs(t, err, &de)
	require.Contains(t, de.Reason, "unknown payload schema")
}

// TestWorkerWantedScanQueuesDonorSearchesUnderACap (anime dual-audio spec
// §6.1): a sweep publishes a donor search for each item whose file lacks a
// grafted dub, beside any video search, at most DonorSearchesPerSweep of
// them live -- a pool of searches is never fired at an indexer at once --
// and the ones searched before from the local release index.
func TestWorkerWantedScanQueuesDonorSearchesUnderACap(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-wantedscan-donors")
	pub := &recordingPublisher{}
	f.worker.Publisher = pub
	f.worker.DonorSearchesPerSweep = 2

	profile := "wf-worker-wantedscan-donors"
	searched := metav1.NewTime(time.Now().Add(-30 * 24 * time.Hour))
	for i, name := range []string{"monster-a", "monster-b", "monster-c", "monster-searched"} {
		createMovie(t, ctx, f.mgr, f.ns, name, profile)
		status := catalogac.MovieStatus().WithPhase(catalogv1alpha1.MoviePhaseImported).
			WithAudio(catalogac.AudioState().WithWanted("en", "ja").WithPresent("ja").WithMissing("en").WithGraft("searching"))
		_, err := k8s.PatchStatus(ctx, f.mgr, k8s.ManagerCatalog, catalogac.Movie(name, f.ns).WithStatus(status))
		require.NoError(t, err)
		if i == 3 {
			_, err = k8s.PatchStatus(ctx, f.mgr, k8s.ManagerCatalogGrab, catalogac.Movie(name, f.ns).WithStatus(
				catalogac.MovieStatus().WithDonorSearchAttempts(commonv1.Attempts{Latest: &searched, Count: 1})))
			require.NoError(t, err)
		}
	}
	eventually(t, 10*time.Second, "the audio states to reach the cache", func() bool {
		var list catalogv1alpha1.MovieList
		if err := f.mgr.List(ctx, &list, client.InNamespace(f.ns)); err != nil {
			return false
		}
		n := 0
		for i := range list.Items {
			if list.Items[i].Status.Audio != nil && (list.Items[i].Name != "monster-searched" || list.Items[i].Status.DonorSearchAttempts.Count == 1) {
				n++
			}
		}
		return n == 4
	})

	require.NoError(t, f.worker.Handle(ctx, testMessage{env: wantedScanEnvelope(t, schema.WantedScan{Namespace: f.ns, Epoch: 7})}))
	_, envs := pub.snapshot()
	live, indexed := 0, 0
	for _, env := range envs {
		var task schema.SearchTask
		require.NoError(t, json.Unmarshal(env.Data, &task))
		require.Equal(t, schema.SearchPurposeAudioDonor, task.Purpose, "an Imported movie gets only a donor search")
		require.Contains(t, env.ID, "donor-search")
		if task.IndexOnly {
			indexed++
			require.Equal(t, "monster-searched", task.MediaRef.Name)
		} else {
			live++
		}
	}
	require.Equal(t, 2, live, "the live donor searches are capped")
	require.Equal(t, 1, indexed, "a donor searched before is looked up in the release index")
}

// TestADonorSearchWithNothingToAlignOnStillCounts: an item whose original
// language is unknown can have no donor aligned to it; its skipped donor
// search is recorded all the same, so its backoff grows and it stops
// taking one of the sweep's live donor searches every time (final review).
func TestADonorSearchWithNothingToAlignOnStillCounts(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-donor-unknown")
	_, err := k8s.PatchStatus(ctx, f.mgr, k8s.ManagerCatalog, catalogac.Movie("the-matrix", f.ns).WithStatus(
		catalogac.MovieStatus().WithPhase(catalogv1alpha1.MoviePhaseImported).
			WithAudio(catalogac.AudioState().WithWanted("en").WithPresent("ja").WithMissing("en").WithGraft("searching"))))
	require.NoError(t, err)
	eventually(t, 10*time.Second, "the audio state to reach the cache", func() bool {
		var m catalogv1alpha1.Movie
		return f.mgr.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "the-matrix"}, &m) == nil && m.Status.Audio != nil
	})
	require.NoError(t, f.worker.Handle(ctx, testMessage{env: f.envelope(t, schema.SearchTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:   schema.SearchReasonMissing, Purpose: schema.SearchPurposeAudioDonor,
	})}))
	var m catalogv1alpha1.Movie
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "the-matrix"}, &m))
	require.EqualValues(t, 1, m.Status.DonorSearchAttempts.Count)
}
