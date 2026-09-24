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

package overlayprofile_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/yaml"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/overlayprofile"
	catalogstatus "github.com/mediactl/clustarr/app/catalog/status"
	"github.com/mediactl/clustarr/app/catalog/worker/artwork"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestStatusHashChangesWithEveryRenderField holds status.hash to spec §C.4:
// "a test fails if a render field stops moving it". Every field of
// OverlayProfileSpec is classified here -- a field added later fails the
// test until it is -- and each render field, down to every geometry
// percentage, must move the hash, while the selection fields must not: a
// relabel must not re-render a library.
func TestStatusHashChangesWithEveryRenderField(t *testing.T) {
	render := map[string]bool{"Badges": true, "Corner": true, "Geometry": true}
	selection := map[string]func(*catalogv1alpha1.OverlayProfileSpec){
		"Selector": func(s *catalogv1alpha1.OverlayProfileSpec) {
			s.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"a": "b"}}
		},
		"Kinds": func(s *catalogv1alpha1.OverlayProfileSpec) { s.Kinds = []commonv1.MediaKind{commonv1.MediaKindMovie} },
	}
	st := reflect.TypeOf(catalogv1alpha1.OverlayProfileSpec{})
	for i := range st.NumField() {
		name := st.Field(i).Name
		_, sel := selection[name]
		assert.True(t, render[name] || sel, "OverlayProfileSpec.%s is neither a render nor a selection field: classify it", name)
	}

	base := overlayprofile.Hash(catalogv1alpha1.OverlayProfileSpec{})
	for name, set := range selection {
		var s catalogv1alpha1.OverlayProfileSpec
		set(&s)
		assert.Equal(t, base, overlayprofile.Hash(s), "%s decides which items, not what is drawn", name)
	}

	// Corner: every non-default corner moves it; the default is unset.
	for _, c := range []catalogv1alpha1.OverlayCorner{
		catalogv1alpha1.OverlayCornerBottomLeft, catalogv1alpha1.OverlayCornerTopRight, catalogv1alpha1.OverlayCornerTopLeft,
	} {
		assert.NotEqual(t, base, overlayprofile.Hash(catalogv1alpha1.OverlayProfileSpec{Corner: c}), "corner %s", c)
	}
	assert.Equal(t, base, overlayprofile.Hash(catalogv1alpha1.OverlayProfileSpec{Corner: catalogv1alpha1.OverlayCornerBottomRight}))

	// Badges: which, and in what order; unset is the one metacritic badge.
	badges := func(srcs ...catalogv1alpha1.RatingSource) catalogv1alpha1.OverlayProfileSpec {
		var s catalogv1alpha1.OverlayProfileSpec
		for _, src := range srcs {
			s.Badges = append(s.Badges, catalogv1alpha1.OverlayBadge{Source: src})
		}
		return s
	}
	assert.Equal(t, base, overlayprofile.Hash(badges(catalogv1alpha1.RatingSourceMetacritic)))
	imdb := overlayprofile.Hash(badges(catalogv1alpha1.RatingSourceIMDb))
	assert.NotEqual(t, base, imdb)
	ab := overlayprofile.Hash(badges(catalogv1alpha1.RatingSourceIMDb, catalogv1alpha1.RatingSourceTMDB))
	ba := overlayprofile.Hash(badges(catalogv1alpha1.RatingSourceTMDB, catalogv1alpha1.RatingSourceIMDb))
	assert.NotEqual(t, imdb, ab)
	assert.NotEqual(t, ab, ba, "the stack's order is drawn")

	// Geometry: every percentage, by reflection, so a new one is covered.
	gt := reflect.TypeOf(catalogv1alpha1.OverlayGeometry{})
	for i := range gt.NumField() {
		f := gt.Field(i)
		require.Equal(t, reflect.TypeOf((*int32)(nil)), f.Type, "OverlayGeometry.%s is not an *int32; extend this test", f.Name)
		// The accessor's default, read back through an empty geometry.
		def := geometryDefault(t, f.Name)

		g := catalogv1alpha1.OverlayGeometry{}
		reflect.ValueOf(&g).Elem().Field(i).Set(reflect.ValueOf(ptr.To(def + 1)))
		assert.NotEqual(t, base, overlayprofile.Hash(catalogv1alpha1.OverlayProfileSpec{Geometry: &g}),
			"geometry.%s stopped moving status.hash", f.Name)

		same := catalogv1alpha1.OverlayGeometry{}
		reflect.ValueOf(&same).Elem().Field(i).Set(reflect.ValueOf(ptr.To(def)))
		assert.Equal(t, base, overlayprofile.Hash(catalogv1alpha1.OverlayProfileSpec{Geometry: &same}),
			"geometry.%s set to its default hashes as unset", f.Name)
	}

	// status.hash is the renderer's hash: one conversion, not two.
	s := badges(catalogv1alpha1.RatingSourceTMDB)
	assert.Equal(t, artwork.ProfileHash(s), overlayprofile.Hash(s))
}

func geometryDefault(t *testing.T, field string) int32 {
	t.Helper()
	m := reflect.ValueOf((*catalogv1alpha1.OverlayGeometry)(nil)).MethodByName(field + "OrDefault")
	require.True(t, m.IsValid(), "OverlayGeometry.%s has no %sOrDefault accessor", field, field)
	return m.Call(nil)[0].Interface().(int32)
}

var (
	envOnce   sync.Once
	envClient client.Client
	envErr    error
)

func envtestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	envOnce.Do(func() {
		env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
		cfg, err := env.Start()
		if err != nil {
			envErr = err
			return
		}
		envClient, envErr = client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	})
	require.NoError(t, envErr)
	return envClient
}

// renderCollector records every RenderOverlayTask delivered on a membus.
type renderCollector struct {
	mu   sync.Mutex
	envs []*events.Envelope
	subj []string
}

func collectRenders(t *testing.T, bus events.Bus) *renderCollector {
	t.Helper()
	c := &renderCollector{}
	stop, err := bus.Subscribe(context.Background(), events.Subscription{
		Stream: events.StreamWorkCatalogarr, Durable: "test-render-collector",
		Filters: []string{events.FilterCatalogArtworkRender},
	}, func(_ context.Context, m events.Message) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.envs = append(c.envs, m.Envelope())
		c.subj = append(c.subj, m.Subject())
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(stop)
	return c
}

// settled waits for want deliveries, then a little longer to see that no
// more arrive, and returns them.
func (c *renderCollector) settled(t *testing.T, want int) ([]*events.Envelope, []string) {
	t.Helper()
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.envs) >= want
	}, 5*time.Second, 5*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*events.Envelope(nil), c.envs...), append([]string(nil), c.subj...)
}

type world struct {
	t   *testing.T
	c   client.Client
	ns  string
	bus events.Bus
	r   *overlayprofile.Reconciler
}

func newWorld(t *testing.T, ns string) *world {
	t.Helper()
	c := envtestClient(t)
	require.NoError(t, c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(context.Background(), events.Default()))
	return &world{t: t, c: c, ns: ns, bus: bus, r: &overlayprofile.Reconciler{Client: c, Bus: bus}}
}

func (w *world) profile(name string, sel map[string]string, mutate ...func(*catalogv1alpha1.OverlayProfile)) {
	w.t.Helper()
	p := &catalogv1alpha1.OverlayProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: w.ns},
		Spec: catalogv1alpha1.OverlayProfileSpec{
			Badges: []catalogv1alpha1.OverlayBadge{{Source: catalogv1alpha1.RatingSourceTMDB}},
		},
	}
	if sel != nil {
		p.Spec.Selector = &metav1.LabelSelector{MatchLabels: sel}
	}
	for _, m := range mutate {
		m(p)
	}
	require.NoError(w.t, w.c.Create(context.Background(), p))
}

// movie creates a Movie; rated gives it the gateway's poster entry and a
// tmdb rating, so a profile with a tmdb badge has something to render.
func (w *world) movie(name string, lbls map[string]string, rated bool) *catalogv1alpha1.Movie {
	w.t.Helper()
	ctx := context.Background()
	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: w.ns, Labels: lbls},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 1, QualityProfileRef: "q", RootFolderRef: "r"},
	}
	require.NoError(w.t, w.c.Create(ctx, m))
	if rated {
		_, err := k8s.PatchStatus(ctx, w.c, catalogstatus.GatewayManager, catalogac.Movie(name, w.ns).WithStatus(catalogac.MovieStatus().
			WithMetadata(catalogac.MovieMetadata().WithTitle(name).
				WithRatings(catalogac.Rating().WithSource(catalogv1alpha1.RatingSourceTMDB).WithValueCentis(700))).
			WithArtwork(catalogstatus.ArtworkEntries([]catalogv1alpha1.ArtworkEntry{{
				Type: catalogv1alpha1.ImageTypePoster, Source: catalogv1alpha1.ArtworkSourceProvider,
				SourceURL: "https://img.example/" + name, Digest: "digest-" + name, SizeBytes: 1,
				UpdatedAt: metav1.NewTime(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)),
			}})...)))
		require.NoError(w.t, err)
	}
	require.NoError(w.t, w.c.Get(ctx, client.ObjectKeyFromObject(m), m))
	return m
}

func (w *world) series(name string, lbls map[string]string) {
	w.t.Helper()
	require.NoError(w.t, w.c.Create(context.Background(), &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: w.ns, Labels: lbls},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 1, QualityProfileRef: "q", RootFolderRef: "r"},
	}))
}

func (w *world) reconcile(name string) {
	w.t.Helper()
	_, err := w.r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: w.ns, Name: name}})
	require.NoError(w.t, err)
}

func (w *world) get(name string) *catalogv1alpha1.OverlayProfile {
	w.t.Helper()
	var p catalogv1alpha1.OverlayProfile
	require.NoError(w.t, w.c.Get(context.Background(), types.NamespacedName{Namespace: w.ns, Name: name}, &p))
	return &p
}

func (w *world) item(name string) artwork.Item {
	w.t.Helper()
	var m catalogv1alpha1.Movie
	require.NoError(w.t, w.c.Get(context.Background(), types.NamespacedName{Namespace: w.ns, Name: name}, &m))
	it, err := artwork.ItemOf(&m)
	require.NoError(w.t, err)
	return it
}

// rendered stands in for the renderer: it records the overlay the plan
// wants for the movie, under the renderer's manager.
func (w *world) rendered(name string) {
	w.t.Helper()
	var list catalogv1alpha1.OverlayProfileList
	require.NoError(w.t, w.c.List(context.Background(), &list, client.InNamespace(w.ns)))
	it := w.item(name)
	want := artwork.Plan(it, list.Items, it.PosterDigest)
	require.NotNil(w.t, want.Profile)
	require.NoError(w.t, catalogstatus.PatchOverlay(context.Background(), w.c, catalogstatus.RendererManager, it.Object,
		&catalogv1alpha1.OverlayEntry{
			ProfileRef: want.Profile.Name, Digest: "overlay-" + name, RenderedFrom: want.InputsDigest,
			UpdatedAt: metav1.NewTime(time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)),
		}))
}

func (w *world) cleared(name string) {
	w.t.Helper()
	require.NoError(w.t, catalogstatus.PatchOverlay(context.Background(), w.c, catalogstatus.RendererManager, w.item(name).Object, nil))
}

func (w *world) relabel(name string, lbls map[string]string) {
	w.t.Helper()
	it := w.item(name)
	it.Object.SetLabels(lbls)
	require.NoError(w.t, w.c.Update(context.Background(), it.Object))
}

var critics = map[string]string{"overlay": "critics"}

func condition(p *catalogv1alpha1.OverlayProfile, t string) *metav1.Condition {
	return k8s.FindCondition(p.Status.Conditions, t)
}

func TestTheLowerNameWinsAndTheLoserIsOverlapped(t *testing.T) {
	w := newWorld(t, "overlap")
	w.profile("beta", critics)
	w.profile("alpha", critics)
	w.profile("series-only", map[string]string{"overlay": "audience"}, func(p *catalogv1alpha1.OverlayProfile) {
		p.Spec.Kinds = []commonv1.MediaKind{commonv1.MediaKindSeries}
	})
	w.profile("unset", nil)
	w.movie("heat", critics, false)
	w.movie("ronin", critics, false)
	w.movie("plain", nil, false)
	w.series("lost", critics)
	w.series("fargo", map[string]string{"overlay": "audience"})
	w.movie("audience-movie", map[string]string{"overlay": "audience"}, false) // a movie series-only never selects
	for _, n := range []string{"alpha", "beta", "series-only", "unset"} {
		w.reconcile(n)
	}

	alpha := w.get("alpha")
	assert.EqualValues(t, 3, alpha.Status.Selected, "heat, ronin and lost")
	assert.Equal(t, overlayprofile.Hash(alpha.Spec), alpha.Status.Hash)
	require.NotNil(t, condition(alpha, overlayprofile.ConditionOverlap))
	assert.Equal(t, metav1.ConditionFalse, condition(alpha, overlayprofile.ConditionOverlap).Status)
	assert.True(t, k8s.IsReady(alpha.Status.Conditions))

	beta := w.get("beta")
	assert.EqualValues(t, 0, beta.Status.Selected, "alpha sorts first and wins every item")
	require.NotNil(t, condition(beta, overlayprofile.ConditionOverlap))
	assert.Equal(t, metav1.ConditionTrue, condition(beta, overlayprofile.ConditionOverlap).Status)
	assert.Equal(t, overlayprofile.ReasonSelectorOverlap, condition(beta, overlayprofile.ConditionOverlap).Reason)

	seriesOnly := w.get("series-only")
	assert.EqualValues(t, 1, seriesOnly.Status.Selected, "fargo; not the movie with the same label")
	assert.Equal(t, metav1.ConditionFalse, condition(seriesOnly, overlayprofile.ConditionOverlap).Status)

	unset := w.get("unset")
	assert.EqualValues(t, 0, unset.Status.Selected, "a nil selector selects nothing")
	assert.True(t, k8s.IsReady(unset.Status.Conditions))

	// The status is the controller's one apply under catalogarr.
	for _, e := range alpha.ManagedFields {
		if e.Subresource == "status" {
			assert.Equal(t, string(k8s.ManagerCatalogarr), e.Manager)
		}
	}
}

func TestAnInvalidSelectorIsNotReady(t *testing.T) {
	w := newWorld(t, "invalid-selector")
	// The apiserver validates a LabelSelector's shape but not a MatchLabels
	// value's syntax on a CRD; a value no label can hold is the invalid case.
	w.profile("bad", nil, func(p *catalogv1alpha1.OverlayProfile) {
		p.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"overlay": "not a valid value!"}}
	})
	w.reconcile("bad")
	bad := w.get("bad")
	assert.False(t, k8s.IsReady(bad.Status.Conditions))
	assert.Equal(t, overlayprofile.ReasonInvalidSelector, condition(bad, "Ready").Reason)
	assert.EqualValues(t, 0, bad.Status.Selected)
}

func TestOneRenderTaskPerSelectedItem(t *testing.T) {
	w := newWorld(t, "publish")
	got := collectRenders(t, w.bus)
	w.profile("critics", critics)
	heat := w.movie("heat", critics, true)
	ronin := w.movie("ronin", critics, true)
	w.movie("plain", nil, true)              // not selected
	w.movie("no-poster-yet", critics, false) // selected, nothing to draw: the gateway publishes when it lands
	w.reconcile("critics")

	envs, subjects := got.settled(t, 2)
	require.Len(t, envs, 2, "one task per selected item with something to render")
	byName := map[string]*events.Envelope{}
	for i, env := range envs {
		var task schema.RenderOverlayTask
		require.NoError(t, schema.Decode(env.Schema, env.Data, &task))
		assert.Equal(t, commonv1.MediaKindMovie, task.MediaRef.Kind)
		assert.Equal(t, artwork.ReasonProfile, task.Reason)
		assert.Equal(t, w.ns+"/"+task.MediaRef.Name, env.Key)
		assert.Equal(t, events.WorkArtworkRenderSubject(events.MediaKey("movie", w.ns, task.MediaRef.Name)), subjects[i])
		byName[task.MediaRef.Name] = env
	}
	for _, m := range []*catalogv1alpha1.Movie{heat, ronin} {
		require.Contains(t, byName, m.Name)
		assert.True(t, strings.HasPrefix(byName[m.Name].ID, string(m.UID)+"/render/"), byName[m.Name].ID)
	}
	assert.EqualValues(t, 3, w.get("critics").Status.Selected)

	// A hot reconcile publishes nothing new, and a rendered item nothing
	// at all.
	w.rendered("heat")
	w.reconcile("critics")
	w.reconcile("critics")
	envs, _ = got.settled(t, 2)
	assert.Len(t, envs, 2)
}

// An item this profile rendered but no longer selects -- relabelled, or the
// profile deleted -- gets a task so the renderer removes its overlay (spec
// §C.4: "an item matched by no non-overlapped profile has its overlay
// removed on its next render task").
func TestAnItemNoLongerSelectedIsReleased(t *testing.T) {
	w := newWorld(t, "release")
	got := collectRenders(t, w.bus)
	w.profile("critics", critics)
	w.movie("heat", critics, true)
	w.movie("ronin", critics, true)
	w.rendered("heat")
	w.rendered("ronin")
	w.reconcile("critics")
	envs, _ := got.settled(t, 0)
	require.Empty(t, envs, "both overlays are current")

	w.relabel("heat", nil)
	w.reconcile("critics")
	envs, _ = got.settled(t, 1)
	require.Len(t, envs, 1)
	var task schema.RenderOverlayTask
	require.NoError(t, schema.Decode(envs[0].Schema, envs[0].Data, &task))
	assert.Equal(t, "heat", task.MediaRef.Name)
	assert.EqualValues(t, 1, w.get("critics").Status.Selected)

	// Deleting the profile releases every item it still names.
	require.NoError(t, w.c.Delete(context.Background(), w.get("critics")))
	w.reconcile("critics")
	envs, _ = got.settled(t, 2)
	require.Len(t, envs, 2, "ronin's overlay names the deleted profile, heat's too until the renderer clears it")
	require.NoError(t, schema.Decode(envs[1].Schema, envs[1].Data, &task))
	assert.Contains(t, []string{"heat", "ronin"}, task.MediaRef.Name)
}

// A state that returns to one already published inside the bus's duplicate
// window is published again: a label removed and restored must re-render,
// not be absorbed as a duplicate of the first task while the item has no
// overlay. The Msg-Id carries the item's resourceVersion (Token).
func TestAFlipBackIsNotAbsorbedAsADuplicate(t *testing.T) {
	w := newWorld(t, "flip-back")
	got := collectRenders(t, w.bus)
	w.profile("critics", critics)
	w.movie("heat", critics, true)

	w.reconcile("critics") // the first render task
	envs, _ := got.settled(t, 1)
	require.Len(t, envs, 1)
	w.rendered("heat")

	w.relabel("heat", nil) // the release task
	w.reconcile("critics")
	envs, _ = got.settled(t, 2)
	require.Len(t, envs, 2)
	w.cleared("heat")

	w.relabel("heat", critics) // back: the same wanted overlay as the first task
	w.reconcile("critics")
	envs, _ = got.settled(t, 3)
	require.Len(t, envs, 3, "the restored label's render was absorbed as a duplicate of the first")
	assert.NotEqual(t, envs[0].ID, envs[2].ID)

	// And a hot loop over the unchanged item still publishes once.
	w.reconcile("critics")
	w.reconcile("critics")
	envs, _ = got.settled(t, 3)
	assert.Len(t, envs, 3)
}

// config/samples/catalog_v1alpha1_overlayprofile.yaml decodes strictly, is
// admitted by the CRD and reconciles Ready, selecting the item it tells the
// reader to label (config/samples/README.md: a sample is applied against a
// running controller in the change that adds it).
func TestTheSampleReconciles(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "config", "samples", "catalog_v1alpha1_overlayprofile.yaml"))
	require.NoError(t, err)
	var p catalogv1alpha1.OverlayProfile
	require.NoError(t, yaml.UnmarshalStrict(raw, &p))

	w := newWorld(t, "sample")
	p.Namespace = w.ns
	require.NoError(t, w.c.Create(context.Background(), &p))
	w.movie("heat", p.Spec.Selector.MatchLabels, true)
	w.reconcile(p.Name)

	got := w.get(p.Name)
	assert.True(t, k8s.IsReady(got.Status.Conditions))
	assert.EqualValues(t, 1, got.Status.Selected)
	assert.Equal(t, overlayprofile.Hash(got.Spec), got.Status.Hash)
}
