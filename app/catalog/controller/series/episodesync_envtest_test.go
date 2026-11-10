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

package series_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
)

// countingEpisodeRPC is fakeEpisodeRPC counting the episode lists the
// reconciler asks the gateway for.
type countingEpisodeRPC struct {
	fakeEpisodeRPC
	calls atomic.Int32
}

func (c *countingEpisodeRPC) Request(ctx context.Context, subject string, in, out any) error {
	c.calls.Add(1)
	return c.fakeEpisodeRPC.Request(ctx, subject, in, out)
}

// pilotOnly is the one-episode list the gateway answers with unless a test
// says otherwise.
var pilotOnly = []metadata.Episode{{SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot", Runtime: 42}}

// episodeSyncFixture is a Series added under monitor, the test reconciles
// by hand through the cache-only manager, with a counting gateway answering
// episodes behind it and a recorder of every subject the reconciler
// publishes to.
func episodeSyncFixture(t *testing.T, ctx context.Context, ns, name string, monitor catalogv1alpha1.SeriesMonitorMode, episodes []metadata.Episode) (client.Client, *series.Reconciler, *countingEpisodeRPC, *subjectRecorder, types.NamespacedName) {
	t.Helper()
	cfg := newTestConfig(t)
	c := startCacheOnly(t, ctx, cfg)
	require.NoError(t, c.Create(ctx, testNamespace(ns)))
	require.NoError(t, c.Create(ctx, testRootFolder(ns, "tv-root", "/data/media/tv")))
	rpc := &countingEpisodeRPC{fakeEpisodeRPC: fakeEpisodeRPC{episodes: episodes}}
	pub := &subjectRecorder{}
	r := &series.Reconciler{
		Client: c, Scheme: k8s.MustNewScheme(), Recorder: k8sevents.NewFakeRecorder(32),
		Bus: combinedBus{Publisher: pub, requester: rpc},
	}
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.SeriesSpec{
			TvdbID: 81189, QualityProfileRef: "none", RootFolderRef: "tv-root",
			AddOptions: catalogv1alpha1.SeriesAddOptions{Monitor: monitor},
		},
	}))
	return c, r, rpc, pub, types.NamespacedName{Namespace: ns, Name: name}
}

// episodesOf lists the Episodes of the series through the cache.
func episodesOf(t *testing.T, ctx context.Context, c client.Client, nn types.NamespacedName) int {
	t.Helper()
	return len(episodesByNumber(t, ctx, c, nn))
}

// episodesByNumber is the series' Episodes by {season, episode}.
func episodesByNumber(t *testing.T, ctx context.Context, c client.Client, nn types.NamespacedName) map[[2]int32]catalogv1alpha1.Episode {
	t.Helper()
	var eps catalogv1alpha1.EpisodeList
	require.NoError(t, c.List(ctx, &eps, client.InNamespace(nn.Namespace)))
	out := map[[2]int32]catalogv1alpha1.Episode{}
	for _, ep := range eps.Items {
		if ep.Spec.SeriesRef == nn.Name {
			out[[2]int32{ep.Spec.SeasonNumber, ep.Spec.EpisodeNumber}] = ep
		}
	}
	return out
}

// A Series without metadata asks the gateway for no episode list (design
// D2, §5.1, as the Artist, Author and Comic wait): its fan-out is keyed on
// the metadata refresh, so a pass before the document had nothing to key
// on and fetched the list on every wake. It reads EpisodesSynced False,
// MetadataPending, until the document is cached, and the first pass after
// that lists the episodes.
func TestASeriesWithoutMetadataAsksForNoEpisodeList(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, r, rpc, _, nn := episodeSyncFixture(t, ctx, "episodesync-pending-ns", "breaking-bad", catalogv1alpha1.SeriesMonitorAll, pilotOnly)

	_, err := r.ReconcileItem(ctx, nn)
	require.NoError(t, err)
	assert.Zero(t, rpc.calls.Load(), "no metadata, no episode list")

	var got catalogv1alpha1.Series
	require.Eventually(t, func() bool {
		return c.Get(ctx, nn, &got) == nil &&
			k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.SeriesConditionEpisodesSynced) != nil
	}, 5*time.Second, 10*time.Millisecond, "the pass never wrote its status")
	cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.SeriesConditionEpisodesSynced)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "MetadataPending", cond.Reason)
	assert.Equal(t, catalogv1alpha1.SeriesPhasePending, got.Status.Phase)
	assert.Zero(t, episodesOf(t, ctx, c, nn))

	// The gateway's document lands: the next pass lists the episodes.
	cacheMetadata(t, ctx, c, nn.Namespace, nn.Name, "Breaking Bad", time.Now().Add(-time.Minute))
	_, err = r.ReconcileItem(ctx, nn)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rpc.calls.Load(), "the first pass with metadata fetches the list")
	require.Eventually(t, func() bool {
		if c.Get(ctx, nn, &got) != nil {
			return false
		}
		cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.SeriesConditionEpisodesSynced)
		return cond != nil && cond.Status == metav1.ConditionTrue
	}, 5*time.Second, 10*time.Millisecond, "EpisodesSynced never turned True")
	assert.Equal(t, 1, episodesOf(t, ctx, c, nn))
}

// Two passes with nothing moved fetch the list once (design D2; owner,
// 2026-10-28: every Series pass asked the gateway for its whole list). The
// memo keys every complete fan-out under the spec generation and the
// metadata refresh alone (childsync.KeyAt; until 2026-11-10 the key also
// carried addOptionsApplied, 7450a5f9 retired it), so the flag the first
// complete fan-out flips is no input of the key and costs no second
// fetch; a metadata refresh moves the key and fetches again.
func TestTwoSeriesPassesWithNothingMovedFetchTheListOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, r, rpc, _, nn := episodeSyncFixture(t, ctx, "episodesync-memo-ns", "severance", catalogv1alpha1.SeriesMonitorAll, pilotOnly)
	refreshed := time.Now().Add(-time.Minute)
	cacheMetadata(t, ctx, c, nn.Namespace, nn.Name, "Severance", refreshed)

	_, err := r.ReconcileItem(ctx, nn)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rpc.calls.Load())
	var got catalogv1alpha1.Series
	require.Eventually(t, func() bool {
		return c.Get(ctx, nn, &got) == nil && got.Status.AddOptionsApplied && got.Status.EpisodeCount == 1
	}, 5*time.Second, 10*time.Millisecond, "the first pass never landed")

	_, err = r.ReconcileItem(ctx, nn)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rpc.calls.Load(), "nothing moved: the Episodes the List found are what a fan-out would leave")
	require.NoError(t, c.Get(ctx, nn, &got))
	cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.SeriesConditionEpisodesSynced)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status, "a pass the memo skips still reads synced")
	assert.EqualValues(t, 1, got.Status.EpisodeCount)

	cacheMetadata(t, ctx, c, nn.Namespace, nn.Name, "Severance", refreshed.Add(time.Second))
	_, err = r.ReconcileItem(ctx, nn)
	require.NoError(t, err)
	assert.EqualValues(t, 2, rpc.calls.Load(), "a metadata refresh is a moved input")
}

// The first fan-out applies the add-time monitor mode though it runs a pass
// after the Series was first reconciled (Sonarr's addOptions.monitor; design
// D2): a pass without metadata flips status.addOptionsApplied for nothing,
// since DesiredEpisodes reads it to choose between the add-time mode and
// spec.monitorNewItems. Until 2026-11-10 every pass sent the flag true, so
// once the fan-out waited for the document the first real one ran with it
// flipped: under unmonitorSpecials every special started monitored, under
// none every episode did (the 2026-09-29 search storm's class). And with the
// flag false through the MetadataPending pass, the Series is announced added
// off observedGeneration, once, not on each such pass.
func TestTheFirstEpisodeFanOutAppliesTheAddTimeMonitorMode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, r, rpc, pub, nn := episodeSyncFixture(t, ctx, "episodesync-monitor-ns", "doctor-who",
		catalogv1alpha1.SeriesMonitorUnmonitorSpecials, []metadata.Episode{
			{SeasonNumber: 0, EpisodeNumber: 1, Title: "The Christmas Invasion", Runtime: 60},
			{SeasonNumber: 1, EpisodeNumber: 1, Title: "Rose", Runtime: 45},
		})

	// The metadata-less pass: no list fetched, and the flag not flipped.
	_, err := r.ReconcileItem(ctx, nn)
	require.NoError(t, err)
	assert.Zero(t, rpc.calls.Load())
	var got catalogv1alpha1.Series
	require.Eventually(t, func() bool {
		return c.Get(ctx, nn, &got) == nil && got.Status.ObservedGeneration == got.Generation
	}, 5*time.Second, 10*time.Millisecond, "the pass never wrote its status")
	assert.False(t, got.Status.AddOptionsApplied, "no fan-out has run: the add-time monitor mode is still owed")

	cacheMetadata(t, ctx, c, nn.Namespace, nn.Name, "Doctor Who", time.Now().Add(-time.Minute))
	_, err = r.ReconcileItem(ctx, nn)
	require.NoError(t, err)
	assert.EqualValues(t, 1, rpc.calls.Load())
	require.Eventually(t, func() bool {
		return c.Get(ctx, nn, &got) == nil && got.Status.AddOptionsApplied
	}, 5*time.Second, 10*time.Millisecond, "the first complete fan-out records addOptionsApplied")

	eps := episodesByNumber(t, ctx, c, nn)
	require.Len(t, eps, 2)
	special, pilot := eps[[2]int32{0, 1}], eps[[2]int32{1, 1}]
	require.NotNil(t, special.Spec.Monitored, "the first fan-out sets spec.monitored on every Episode it creates")
	assert.False(t, *special.Spec.Monitored, "unmonitorSpecials: the special starts unmonitored")
	assert.True(t, ptr.Deref(pilot.Spec.Monitored, true), "unmonitorSpecials: a regular episode starts monitored")
	assert.Equal(t, 1, pub.count("clustarr.evt.catalog.series.added."), "announced added once, on the first pass, not again while the flag waits for the fan-out: %v", pub.subjects)
}
