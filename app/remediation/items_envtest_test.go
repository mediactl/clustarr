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

package remediation_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/itemtest"
	"github.com/mediactl/clustarr/app/remediation/remediationtest"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/records"
)

// countingItem counts the item keys the loop reconciles for one kind and
// watches what watches returns.
type countingItem struct {
	mu      sync.Mutex
	seen    map[types.NamespacedName]int
	watches []rollup.Watch
}

func newCountingItem() *countingItem { return &countingItem{seen: map[types.NamespacedName]int{}} }

func (c *countingItem) ReconcileItem(_ context.Context, nn types.NamespacedName) (reconcile.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen[nn]++
	return reconcile.Result{}, nil
}

func (c *countingItem) Watches() []rollup.Watch { return c.watches }

func (c *countingItem) count(ns, name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen[types.NamespacedName{Namespace: ns, Name: name}]
}

func mediaFile(ns, name string, ref commonv1.MediaRef) *catalogv1alpha1.MediaFile {
	return &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: ref, Path: "/data/media/" + name + ".mkv"},
	}
}

// startItems starts a manager running the item path alone over items
// (itemtest.Start), with the loop's indexes.
func startItems(t *testing.T, items map[remediation.KeyKind]rollup.Item) *remediationtest.Env {
	t.Helper()
	return remediationtest.StartManager(t, func(mgr ctrl.Manager, _ events.Bus) error {
		if err := remediation.RegisterIndexes(context.Background(), mgr.GetFieldIndexer()); err != nil {
			return err
		}
		return itemtest.Start(mgr, items)
	})
}

// The item arm of the MediaFile source (loop spec §3.3 S1, S1'; §2.14
// "Wake-ups") wakes every item a file covers when the file appears or goes,
// when its spec changes, and when an input to the items' rollups moves in a
// status-only write -- and for nothing else, so the loop's own status
// applies do not churn 16,500 items.
func TestTheItemArmWakesTheItemsAFileCovers(t *testing.T) {
	movies, episodes := newCountingItem(), newCountingItem()
	e := startItems(t, map[remediation.KeyKind]rollup.Item{remediation.KindMovie: movies, remediation.KindEpisode: episodes})
	ctx := t.Context()
	remediationtest.MustNamespace(t, ctx, e.Admin, "arm")
	c := e.Admin

	heat := mediaFile("arm", "heat-1080p", commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"})
	require.NoError(t, c.Create(ctx, heat))
	require.Eventually(t, func() bool { return movies.count("arm", "heat") > 0 }, 10*time.Second, 20*time.Millisecond, "a new file wakes its movie")

	// A status write outside every rollup's inputs wakes no item.
	before := movies.count("arm", "heat")
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalog,
		catalogac.MediaFile(heat.Name, heat.Namespace).WithStatus(catalogac.MediaFileStatus().WithProbeVersion(7)))
	require.NoError(t, err)
	require.Never(t, func() bool { return movies.count("arm", "heat") > before }, 2*time.Second, 50*time.Millisecond,
		"a probeVersion write woke the movie")

	// The Transcoded verdict moving in a status-only write does.
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalog,
		catalogac.MediaFile(heat.Name, heat.Namespace).WithStatus(catalogac.MediaFileStatus().WithProbeVersion(7).
			WithMediaInfo(commonv1.MediaInfo{Container: "mkv", VideoCodec: "hevc", TranscodeProfile: "hevc-main10@abc"})))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return movies.count("arm", "heat") > before }, 10*time.Second, 20*time.Millisecond,
		"the Transcoded verdict must reach the movie")

	// A label alone wakes no item.
	before = movies.count("arm", "heat")
	require.NoError(t, c.Patch(ctx, heat.DeepCopy(), client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"labels":{"tier":"hd"}}}`))))
	require.Never(t, func() bool { return movies.count("arm", "heat") > before }, 2*time.Second, 50*time.Millisecond, "a label woke the movie")

	// A file going wakes every item it covered.
	require.NoError(t, c.Delete(ctx, heat))
	require.Eventually(t, func() bool { return movies.count("arm", "heat") > before }, 10*time.Second, 20*time.Millisecond, "a deleted file wakes its movie")
	assert.Zero(t, episodes.count("arm", "heat"), "a movie's file woke an episode key")
}

// TestAMultiEpisodeFileWakesEveryEpisode is loop spec §3.20's guard: one
// file key and N Episode keys. A pack appearing wakes every episode it
// covers; the loop's own status apply that moves an input to their rollups
// (the probe landing) wakes every one of them again, through the loop's
// one controller, and never the file key itself.
func TestAMultiEpisodeFileWakesEveryEpisode(t *testing.T) {
	episodes := newCountingItem()
	probe := remediationtest.ProbeFake(func(v *remediation.View, _ int, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
		out.ProbeHash, out.MediaInfo = "h1", &commonv1.MediaInfo{Container: "mkv", VideoCodec: "h264", Audio: []commonv1.AudioStream{{Language: "eng"}}}
		k8s.MarkTrue(v.File, &out.Conditions, catalogv1alpha1.MediaFileConditionProbed, "Probed", "probed")
		return remediation.Result{}, nil
	})
	e := remediationtest.StartManager(t, func(mgr ctrl.Manager, bus events.Bus) error {
		if err := remediation.RegisterIndexes(context.Background(), mgr.GetFieldIndexer()); err != nil {
			return err
		}
		loop := &remediation.Env{Reader: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Bus: bus,
			IO: remediation.NewIOExecutor(2), Pacer: records.NewPacer(records.DefaultRates(), time.Now)}
		r, err := remediation.NewReconciler(mgr.GetClient(), loop, nil, remediation.Config{Concurrency: 2}, []remediation.Bound{remediation.Bind[int](probe)})
		if err != nil {
			return err
		}
		r.Items = remediation.ItemReconciler{Items: map[remediation.KeyKind]rollup.Item{remediation.KindEpisode: episodes}}
		return r.SetupWithManager(mgr)
	})
	ctx := t.Context()
	remediationtest.MustNamespace(t, ctx, e.Admin, "pack")
	pack := mediaFile("pack", "show-s01e01e02", commonv1.MediaRef{
		Kind: commonv1.MediaKindEpisode, Name: "show-s01e01", Keys: []string{"show-s01e01", "show-s01e02", "show-s01e03"},
	})
	require.NoError(t, e.Admin.Create(ctx, pack))
	all := func(min int) bool {
		return episodes.count("pack", "show-s01e01") >= min && episodes.count("pack", "show-s01e02") >= min && episodes.count("pack", "show-s01e03") >= min
	}
	require.Eventually(t, func() bool { return all(1) }, 10*time.Second, 20*time.Millisecond, "a new pack wakes every episode it covers")
	nn := types.NamespacedName{Namespace: "pack", Name: pack.Name}
	require.Eventually(t, func() bool { return e.Get(t, nn).Status.ProbeHash == "h1" }, 10*time.Second, 20*time.Millisecond, "the loop's probe never landed")
	require.Eventually(t, func() bool { return all(2) }, 10*time.Second, 20*time.Millisecond,
		"the loop's own apply moved the file's signature and did not wake every episode")
	plans := probe.Plans.Load()
	time.Sleep(2 * time.Second)
	assert.LessOrEqual(t, probe.Plans.Load(), plans+1, "the loop's own status apply kept waking the file key")
	assert.Zero(t, episodes.count("pack", "show-s01e01e02"), "the file's own name reached the item path")
}

// TestAnEpisodeHasFileFlipWakesItsSeriesKey (ADR-0019 A3.2): Series is an
// item key; its kind's watch on Episodes maps an episode whose hasFile or
// phase moved to the Series key, through the item path's controller, and an
// Episode status write that moves neither wakes nothing.
func TestAnEpisodeHasFileFlipWakesItsSeriesKey(t *testing.T) {
	shows := newCountingItem()
	shows.watches = (&series.Reconciler{}).Watches()[1:2] // the Episode watch, the real map and predicate
	require.IsType(t, &catalogv1alpha1.Episode{}, shows.watches[0].Object)
	e := startItems(t, map[remediation.KeyKind]rollup.Item{remediation.KindSeries: shows})
	ctx := t.Context()
	remediationtest.MustNamespace(t, ctx, e.Admin, "flip")
	remediationtest.MustEpisode(t, ctx, e.Admin, "flip", "show-s01e01", "show", 1, 1)
	remediationtest.MustEpisode(t, ctx, e.Admin, "flip", "other-s01e01", "other", 1, 1)
	apply := func(name string, st *catalogac.EpisodeStatusApplyConfiguration) {
		t.Helper()
		_, err := k8s.PatchStatus(ctx, e.Admin, k8s.ManagerCatalog, catalogac.Episode(name, "flip").WithStatus(st))
		require.NoError(t, err)
	}
	apply("show-s01e01", catalogac.EpisodeStatus().WithPhase(catalogv1alpha1.EpisodePhaseWanted))
	require.Eventually(t, func() bool { return shows.count("flip", "show") > 0 }, 10*time.Second, 20*time.Millisecond, "setup: the phase change woke the series")
	before, other := shows.count("flip", "show"), shows.count("flip", "other")

	apply("show-s01e01", catalogac.EpisodeStatus().WithPhase(catalogv1alpha1.EpisodePhaseWanted).WithTitle("Pilot"))
	require.Never(t, func() bool { return shows.count("flip", "show") > before }, 2*time.Second, 50*time.Millisecond,
		"a title write woke the series")

	apply("show-s01e01", catalogac.EpisodeStatus().WithPhase(catalogv1alpha1.EpisodePhaseWanted).WithTitle("Pilot").WithHasFile(true))
	require.Eventually(t, func() bool { return shows.count("flip", "show") > before }, 10*time.Second, 20*time.Millisecond,
		"a hasFile flip did not wake its series")
	assert.Equal(t, other, shows.count("flip", "other"), "another series woke")
	assert.Zero(t, shows.count("flip", "show-s01e01"), "the episode's own key reached the series path")
}
