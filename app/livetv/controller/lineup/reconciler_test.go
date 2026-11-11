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

package lineup

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	clustarrac "github.com/mediactl/clustarr/api/applyconfiguration/clustarr/v1alpha1"
	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/index"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/iptv"
	"github.com/mediactl/clustarr/pkg/iptv/lineup"
	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
	"github.com/mediactl/clustarr/pkg/iptv/m3u"
)

const (
	testNS       = "media"
	testProvider = "argon"
)

var testKey = types.NamespacedName{Namespace: testNS, Name: testProvider}

func category(name string, base int32, names ...string) clustarrv1.LineupCategory {
	return clustarrv1.LineupCategory{Name: name, Base: ptr.To(base), Names: names}
}

func provider(enabled bool, cats ...clustarrv1.LineupCategory) *clustarrv1.IPTVProvider {
	return &clustarrv1.IPTVProvider{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNS, Name: testProvider, Generation: 1},
		Spec:       clustarrv1.IPTVProviderSpec{Lineup: &clustarrv1.LineupSpec{Enabled: enabled, Categories: cats}},
	}
}

// entry is a playlist entry keyed as the m3u parser keys one without a
// tvg-id.
func entry(name string) m3u.Entry {
	return m3u.Entry{Key: m3u.NameKey("G", name), Name: name, Group: "G"}
}

func keyOf(name string) string { return m3u.NameKey("G", name) }

// channel is an existing IPTVChannel of the provider for name's entry, made
// by the ui (its name and origin label), at number.
func channel(name, number string, active bool, mutate ...func(*clustarrv1.IPTVChannel)) *clustarrv1.IPTVChannel {
	ch := &clustarrv1.IPTVChannel{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: testNS, Name: iptv.ChannelObjectName(testProvider, keyOf(name)),
			Labels: map[string]string{clustarrv1.LabelOrigin: clustarrv1.OriginUI},
		},
		Spec: clustarrv1.IPTVChannelSpec{ProviderRef: testProvider, Key: keyOf(name), Number: number, Active: active},
	}
	for _, m := range mutate {
		m(ch)
	}
	return ch
}

type fixture struct {
	t        *testing.T
	r        *Reconciler
	c        client.Client
	store    events.ObjectStore
	rec      *k8sevents.FakeRecorder
	now      time.Time
	entries  []m3u.Entry
	ok       bool
	asked    int
	woken    []types.NamespacedName
	provider *clustarrv1.IPTVProvider
}

func newFixture(t *testing.T, p *clustarrv1.IPTVProvider, channels ...*clustarrv1.IPTVChannel) *fixture {
	t.Helper()
	return newFixtureWith(t, interceptor.Funcs{}, p, channels...)
}

// newFixtureWith is newFixture with the reconciler's client intercepted by
// funcs (Apply defaults to applyKeepingFalse); the reconciler's APIReader
// and the test's reads go to the fake underneath.
func newFixtureWith(t *testing.T, funcs interceptor.Funcs, p *clustarrv1.IPTVProvider, channels ...*clustarrv1.IPTVChannel) *fixture {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, clustarrv1.AddToScheme(s))
	objs := []client.Object{p}
	for _, ch := range channels {
		objs = append(objs, ch)
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).
		WithStatusSubresource(&clustarrv1.IPTVChannel{}, &clustarrv1.IPTVProvider{}).
		WithIndex(&clustarrv1.IPTVChannel{}, index.ChannelByProvider, index.Extract).
		Build()
	if funcs.Apply == nil {
		funcs.Apply = applyKeepingFalse
	}
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	f := &fixture{
		t: t, c: c, store: bus.ObjectStore(events.BucketLiveTV), rec: k8sevents.NewFakeRecorder(64),
		now: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC), ok: true, provider: p,
	}
	f.r = &Reconciler{
		Client: interceptor.NewClient(c, funcs), APIReader: c, Store: f.store, Recorder: f.rec,
		Entries: func(key types.NamespacedName) ([]m3u.Entry, bool) {
			f.asked++
			if key != testKey {
				return nil, false
			}
			return slices.Clone(f.entries), f.ok
		},
		Now:  func() time.Time { return f.now },
		Wake: func(key types.NamespacedName) { f.woken = append(f.woken, key) },
	}
	return f
}

// applyKeepingFalse is the fake client's Apply with spec.active: false
// kept. The fake round-trips an apply through the typed IPTVChannel, whose
// omitempty drops a false Active, so the apply leaves the old true in
// place; the apiserver applies the configuration's JSON as sent, false
// included (reconciler_envtest_test.go holds that). The shim re-sends the
// false the apply carried as a merge patch.
func applyKeepingFalse(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
	ac, ok := obj.(*clustarrac.IPTVChannelApplyConfiguration)
	deactivate := ok && ac.Spec != nil && ac.Spec.Active != nil && !*ac.Spec.Active
	var name, namespace string
	if deactivate {
		name, namespace = *ac.Name, *ac.Namespace
	}
	if err := c.Apply(ctx, obj, opts...); err != nil || !deactivate {
		return err
	}
	ch := &clustarrv1.IPTVChannel{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	return c.Patch(ctx, ch, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"active":false}}`)))
}

func (f *fixture) reconcile() ctrl.Result {
	f.t.Helper()
	res, err := f.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: testKey})
	require.NoError(f.t, err)
	return res
}

// channels are the provider's IPTVChannels by spec.key.
func (f *fixture) channels() map[string]clustarrv1.IPTVChannel {
	f.t.Helper()
	var list clustarrv1.IPTVChannelList
	require.NoError(f.t, f.c.List(context.Background(), &list, client.InNamespace(testNS)))
	out := map[string]clustarrv1.IPTVChannel{}
	for _, ch := range list.Items {
		out[ch.Spec.Key] = ch
	}
	return out
}

func (f *fixture) plan() livetvstore.LineupPlan {
	f.t.Helper()
	_, rc, err := f.store.Get(context.Background(), livetvstore.LineupPlanObject(testKey))
	require.NoError(f.t, err)
	defer func() { _ = rc.Close() }()
	var lp livetvstore.LineupPlan
	require.NoError(f.t, livetvstore.Decode(rc, &lp))
	return lp
}

func (f *fixture) setProvider(mutate func(*clustarrv1.IPTVProvider)) {
	f.t.Helper()
	var p clustarrv1.IPTVProvider
	require.NoError(f.t, f.c.Get(context.Background(), testKey, &p))
	mutate(&p)
	require.NoError(f.t, f.c.Update(context.Background(), &p))
}

// events drains the recorded Events.
func (f *fixture) events() []string {
	var out []string
	for {
		select {
		case e := <-f.rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func news() clustarrv1.LineupCategory { return category("News", 1000, "NEWS$", "^CNN") }

// TestADryRunWritesThePlanAndStatusButNoSpec: with spec.lineup.enabled
// false the plan is stored as a dry run and every existing channel the plan
// names gets its status.lineup, while no spec moves and no channel is made.
func TestADryRunWritesThePlanAndStatusButNoSpec(t *testing.T) {
	cnn := channel("CNN", "5", false)
	mystery := channel("Mystery", "6", true)
	f := newFixture(t, provider(false, news()), cnn, mystery)
	f.entries = []m3u.Entry{entry("CNN"), entry("BBC NEWS"), entry("Mystery")}

	assert.Equal(t, ctrl.Result{}, f.reconcile())

	lp := f.plan()
	assert.True(t, lp.DryRun)
	assert.Empty(t, lp.Error)
	assert.Equal(t, f.now, lp.PlannedAt.UTC())
	require.Len(t, lp.Channels, 2)

	got := f.channels()
	require.Len(t, got, 2, "a dry run creates no channel")
	assert.Equal(t, cnn.Spec, got[keyOf("CNN")].Spec, "a dry run writes no spec")
	assert.Equal(t, mystery.Spec, got[keyOf("Mystery")].Spec)
	assert.Equal(t, map[string]string{clustarrv1.LabelOrigin: clustarrv1.OriginUI}, got[keyOf("CNN")].Labels)
	assert.Equal(t, &clustarrv1.ChannelLineupStatus{Name: "CNN", Category: "News", Rule: "News[0]", Number: "1002"}, got[keyOf("CNN")].Status.Lineup)
	assert.Equal(t, &clustarrv1.ChannelLineupStatus{Name: "Mystery"}, got[keyOf("Mystery")].Status.Lineup)
	assert.Empty(t, f.events(), "a dry run records no Event")
}

// TestAnEnabledLineupCreatesNumbersAndActivatesChannels: the plan's
// channels are created, numbered and activated; an existing channel keeps
// a person's spec.name and its own origin label while its number moves; a
// created one carries the lineup's label across later applies.
func TestAnEnabledLineupCreatesNumbersAndActivatesChannels(t *testing.T) {
	existing := channel("CNN", "7", false, func(ch *clustarrv1.IPTVChannel) { ch.Spec.Name = "Cable News" })
	f := newFixture(t, provider(true, news()), existing)
	f.entries = []m3u.Entry{entry("CNN"), entry("BBC NEWS"), entry("ABC NEWS")}

	f.reconcile()

	assert.False(t, f.plan().DryRun)
	got := f.channels()
	require.Len(t, got, 3)
	cnn := got[keyOf("CNN")]
	assert.Equal(t, existing.Name, cnn.Name)
	assert.Equal(t, "Cable News", cnn.Spec.Name, "a person's name survives")
	assert.Equal(t, "1003", cnn.Spec.Number, "sorted by the shown name: ABC, BBC, Cable News")
	assert.True(t, cnn.Spec.Active)
	assert.Equal(t, clustarrv1.OriginUI, cnn.Labels[clustarrv1.LabelOrigin], "an existing channel keeps its own label")
	assert.Equal(t, &clustarrv1.ChannelLineupStatus{Name: "Cable News", Category: "News", Rule: "News[0]", Number: "1003"}, cnn.Status.Lineup)

	for name, number := range map[string]string{"ABC NEWS": "1001", "BBC NEWS": "1002"} {
		ch := got[keyOf(name)]
		assert.Equal(t, iptv.ChannelObjectName(testProvider, keyOf(name)), ch.Name, name)
		assert.Equal(t, clustarrv1.IPTVChannelSpec{ProviderRef: testProvider, Key: keyOf(name), Number: number, Active: true}, ch.Spec, name)
		assert.Equal(t, map[string]string{clustarrv1.LabelOrigin: clustarrv1.OriginLineup}, ch.Labels, name)
		assert.Equal(t, &clustarrv1.ChannelLineupStatus{Name: name, Category: "News", Rule: "News[0]", Number: number}, ch.Status.Lineup, name)
	}
	ev := f.events()
	require.Len(t, ev, 1)
	assert.Contains(t, ev[0], "Normal LineupApplied")
	assert.Contains(t, ev[0], "2 created")

	// Nothing changed: nothing written, no Event.
	before := f.channels()
	f.reconcile()
	assert.Equal(t, before, f.channels())
	assert.Empty(t, f.events())

	// A new first entry renumbers the rest; the created ones keep their label.
	f.entries = append(f.entries, entry("AAA NEWS"))
	f.reconcile()
	got = f.channels()
	require.Len(t, got, 4)
	assert.Equal(t, "1002", got[keyOf("ABC NEWS")].Spec.Number)
	assert.Equal(t, "1004", got[keyOf("CNN")].Spec.Number)
	assert.Equal(t, clustarrv1.OriginLineup, got[keyOf("ABC NEWS")].Labels[clustarrv1.LabelOrigin], "a renumbering apply keeps the lineup's label")
	assert.Equal(t, clustarrv1.OriginUI, got[keyOf("CNN")].Labels[clustarrv1.LabelOrigin])
}

// TestAnAbsentEntryIsDeactivatedThenDeletedAfterSevenDays: a channel whose
// key left the playlist is deactivated with status.lineup.missingSince set
// once; seven days on the lineup's own channel is deleted, and one it did
// not make is left inactive for good.
func TestAnAbsentEntryIsDeactivatedThenDeletedAfterSevenDays(t *testing.T) {
	fox := channel("FOX", "9", true)
	f := newFixture(t, provider(true, news()), fox)
	f.entries = []m3u.Entry{entry("CNN"), entry("BBC NEWS")}
	f.reconcile()
	require.Len(t, f.channels(), 3)
	bbcName := f.channels()[keyOf("BBC NEWS")].Name

	gone := f.now
	f.entries = []m3u.Entry{entry("CNN")}
	res := f.reconcile()
	got := f.channels()
	require.Len(t, got, 3)
	for _, name := range []string{"BBC NEWS", "FOX"} {
		ch := got[keyOf(name)]
		assert.False(t, ch.Spec.Active, name)
		require.NotNil(t, ch.Status.Lineup, name)
		require.NotNil(t, ch.Status.Lineup.MissingSince, name)
		assert.Equal(t, gone, ch.Status.Lineup.MissingSince.UTC(), name)
	}
	assert.Equal(t, "9", got[keyOf("FOX")].Spec.Number, "a deactivated channel keeps its number")
	assert.Equal(t, "BBC NEWS", got[keyOf("BBC NEWS")].Status.Lineup.Name, "the shown name stays")
	assert.Equal(t, DeleteAbsentAfter, res.RequeueAfter, "woken when the first absent channel is due")

	f.now = gone.Add(3 * 24 * time.Hour)
	res = f.reconcile()
	got = f.channels()
	require.Len(t, got, 3)
	assert.Equal(t, gone, got[keyOf("BBC NEWS")].Status.Lineup.MissingSince.UTC(), "set once")
	assert.Equal(t, 4*24*time.Hour, res.RequeueAfter)

	f.now = gone.Add(DeleteAbsentAfter + time.Minute)
	f.reconcile()
	got = f.channels()
	require.Len(t, got, 2)
	assert.NotContains(t, got, keyOf("BBC NEWS"))
	var deleted clustarrv1.IPTVChannel
	assert.True(t, apierrors.IsNotFound(f.c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: bbcName}, &deleted)))
	require.Contains(t, got, keyOf("FOX"), "a channel the lineup did not make is never deleted")
	assert.False(t, got[keyOf("FOX")].Spec.Active)
	ev := f.events()
	require.NotEmpty(t, ev)
	assert.Contains(t, ev[len(ev)-1], "1 deleted")

	f.now = f.now.Add(30 * 24 * time.Hour)
	assert.Equal(t, ctrl.Result{}, f.reconcile(), "nothing left to delete: no wake")
	assert.Contains(t, f.channels(), keyOf("FOX"))
}

// TestAnEntryBackWithinSevenDaysKeepsItsChannel: an entry that leaves and
// returns inside the seven days finds its own IPTVChannel, a person's edits
// on it kept, and its missingSince cleared.
func TestAnEntryBackWithinSevenDaysKeepsItsChannel(t *testing.T) {
	f := newFixture(t, provider(true, news()))
	f.entries = []m3u.Entry{entry("CNN"), entry("BBC NEWS")}
	f.reconcile()
	uid := f.channels()[keyOf("BBC NEWS")].UID

	f.entries = []m3u.Entry{entry("CNN")}
	f.reconcile()
	f.now = f.now.Add(3 * 24 * time.Hour)
	bbc := f.channels()[keyOf("BBC NEWS")]
	bbc.Spec.Name = "Beeb"
	require.NoError(t, f.c.Update(context.Background(), &bbc))

	f.now = f.now.Add(24 * time.Hour)
	f.entries = []m3u.Entry{entry("CNN"), entry("BBC NEWS")}
	assert.Equal(t, ctrl.Result{}, f.reconcile())
	bbc = f.channels()[keyOf("BBC NEWS")]
	assert.Equal(t, uid, bbc.UID)
	assert.True(t, bbc.Spec.Active)
	assert.Equal(t, "Beeb", bbc.Spec.Name)
	assert.Equal(t, clustarrv1.OriginLineup, bbc.Labels[clustarrv1.LabelOrigin])
	require.NotNil(t, bbc.Status.Lineup)
	assert.Nil(t, bbc.Status.Lineup.MissingSince)
	assert.Equal(t, "Beeb", bbc.Status.Lineup.Name)
}

// TestAnInvalidLineupWritesNothing: a rule that does not compile stores the
// error beside the last good plan and touches no channel; the playlist is
// not even read.
func TestAnInvalidLineupWritesNothing(t *testing.T) {
	f := newFixture(t, provider(true, news()))
	f.entries = []m3u.Entry{entry("CNN"), entry("BBC NEWS")}
	f.reconcile()
	good := f.plan()
	require.Len(t, good.Channels, 2)
	before := f.channels()
	f.events()

	f.setProvider(func(p *clustarrv1.IPTVProvider) {
		p.Spec.Lineup.Categories = append(p.Spec.Lineup.Categories, category("Kids", 5000, "("))
	})
	f.entries = append(f.entries, entry("ABC NEWS"))
	f.now = f.now.Add(time.Hour)
	asked := f.asked
	f.reconcile()

	lp := f.plan()
	assert.Contains(t, lp.Error, "Kids")
	assert.Contains(t, lp.Error, `"("`)
	assert.Equal(t, f.now, lp.PlannedAt.UTC())
	assert.Equal(t, good.Plan, lp.Plan, "the last good plan is kept")
	assert.Equal(t, asked, f.asked, "the playlist is not read")
	assert.Equal(t, before, f.channels(), "no channel touched")
	assert.Empty(t, f.events())
}

// TestAnOverrideCategoryMovesTheChannel: a person's spec.category moves the
// channel into that category's numbers.
func TestAnOverrideCategoryMovesTheChannel(t *testing.T) {
	cnn := channel("CNN", "1001", true, func(ch *clustarrv1.IPTVChannel) { ch.Spec.Category = ptr.To("Sports") })
	f := newFixture(t, provider(true, news(), category("Sports", 2000, "^ESPN")), cnn)
	f.entries = []m3u.Entry{entry("CNN"), entry("ESPN"), entry("ESPN 2")}

	f.reconcile()

	got := f.channels()
	assert.Equal(t, "2001", got[keyOf("CNN")].Spec.Number)
	assert.True(t, got[keyOf("CNN")].Spec.Active)
	assert.Equal(t, ptr.To("Sports"), got[keyOf("CNN")].Spec.Category, "a person's category is theirs")
	assert.Equal(t, &clustarrv1.ChannelLineupStatus{Name: "CNN", Category: "Sports", Rule: "override", Number: "2001"}, got[keyOf("CNN")].Status.Lineup)
	assert.Equal(t, "2002", got[keyOf("ESPN")].Spec.Number)
	assert.Equal(t, "2003", got[keyOf("ESPN 2")].Spec.Number)
}

// TestAnEmptyOverrideDeactivates: spec.category "" takes the channel off
// every DVR: inactive, its number kept, the reason in status.
func TestAnEmptyOverrideDeactivates(t *testing.T) {
	cnn := channel("CNN", "1001", true, func(ch *clustarrv1.IPTVChannel) { ch.Spec.Category = ptr.To("") })
	f := newFixture(t, provider(true, news()), cnn)
	f.entries = []m3u.Entry{entry("CNN"), entry("BBC NEWS")}

	f.reconcile()

	got := f.channels()
	ch := got[keyOf("CNN")]
	assert.False(t, ch.Spec.Active)
	assert.Equal(t, "1001", ch.Spec.Number)
	assert.Equal(t, ptr.To(""), ch.Spec.Category)
	assert.Equal(t, &clustarrv1.ChannelLineupStatus{Name: "CNN"}, ch.Status.Lineup)
	assert.Equal(t, "1001", got[keyOf("BBC NEWS")].Spec.Number, "the off channel takes no number")
}

// TestTooManyDeactivatesTheCategory: a category over spec.dvr.maxChannels
// is refused whole: its existing channels are deactivated with their
// numbers kept, none of it is created, the other categories go on, and the
// provider gets a LineupCategoryTooMany Event.
func TestTooManyDeactivatesTheCategory(t *testing.T) {
	cnn := channel("CNN", "1001", true)
	p := provider(true, news(), category("Kids", 5000, "^KIDS"))
	p.Spec.DVR.MaxChannels = ptr.To(int32(2))
	f := newFixture(t, p, cnn)
	f.entries = []m3u.Entry{entry("CNN"), entry("BBC NEWS"), entry("ABC NEWS"), entry("KIDS TV")}

	f.reconcile()

	got := f.channels()
	require.Len(t, got, 2, "no channel of the refused category is created")
	assert.False(t, got[keyOf("CNN")].Spec.Active)
	assert.Equal(t, "1001", got[keyOf("CNN")].Spec.Number)
	assert.Equal(t, &clustarrv1.ChannelLineupStatus{Name: "CNN"}, got[keyOf("CNN")].Status.Lineup)
	assert.Equal(t, "5001", got[keyOf("KIDS TV")].Spec.Number)
	assert.True(t, got[keyOf("KIDS TV")].Spec.Active)
	assert.True(t, f.plan().Categories[0].TooMany)

	ev := f.events()
	var tooMany []string
	for _, e := range ev {
		if strings.HasPrefix(e, "Warning LineupCategoryTooMany ") {
			tooMany = append(tooMany, e)
		}
	}
	require.Len(t, tooMany, 1, "%v", ev)
	assert.Contains(t, tooMany[0], `"News"`)

	// The same plan again: no second Event.
	f.reconcile()
	assert.Empty(t, f.events())
}

// TestNoPlaylistYetRequeues: before the tuner's first fetch nothing is
// planned or written, and the provider is asked about again in 30 s.
func TestNoPlaylistYetRequeues(t *testing.T) {
	f := newFixture(t, provider(true, news()))
	f.ok = false
	assert.Equal(t, ctrl.Result{RequeueAfter: 30 * time.Second}, f.reconcile())
	_, _, err := f.store.Get(context.Background(), livetvstore.LineupPlanObject(testKey))
	assert.ErrorIs(t, err, events.ErrObjectNotFound)
	assert.Empty(t, f.channels())
}

// TestAProviderWithoutALineupIsLeftAlone: no spec.lineup, or a disabled
// provider, is no work and no wake.
func TestAProviderWithoutALineupIsLeftAlone(t *testing.T) {
	p := provider(true, news())
	p.Spec.Lineup = nil
	f := newFixture(t, p, channel("CNN", "5", false))
	f.entries = []m3u.Entry{entry("CNN")}
	assert.Equal(t, ctrl.Result{}, f.reconcile())
	assert.Zero(t, f.asked)
	assert.Nil(t, f.channels()[keyOf("CNN")].Status.Lineup)

	p = provider(true, news())
	p.Spec.Enabled = ptr.To(false)
	f = newFixture(t, p)
	f.entries = []m3u.Entry{entry("CNN")}
	assert.Equal(t, ctrl.Result{}, f.reconcile())
	assert.Empty(t, f.channels())
}

// TestAPlanWriteWakesTheProviderController: the provider controller reads
// the plan object, and a write that moves no channel would reach it only at
// the tuner's next render, so every reconcile that writes the plan (an
// error included) or a channel wakes it; one that writes nothing does not.
func TestAPlanWriteWakesTheProviderController(t *testing.T) {
	f := newFixture(t, provider(false, news()))
	f.entries = []m3u.Entry{entry("CNN")}
	f.reconcile()
	assert.Equal(t, []types.NamespacedName{testKey}, f.woken, "a dry run with no channel still wakes it")

	f.woken = nil
	f.reconcile()
	assert.Equal(t, []types.NamespacedName{testKey}, f.woken, "every pass writes the plan, so every pass wakes it")
	f.woken = nil

	f.setProvider(func(p *clustarrv1.IPTVProvider) {
		p.Spec.Lineup.Categories = append(p.Spec.Lineup.Categories, category("Kids", 5000, "("))
	})
	f.reconcile()
	assert.Equal(t, []types.NamespacedName{testKey}, f.woken, "an error plan wakes it")
}

// TestAChannelMapOverTheByteBudgetIsTooMany: a category whose channel map
// would be over blocks.DefaultMaxBytes is refused as one over its count is,
// though it is under spec.dvr.maxChannels: 390 five-digit numbers are
// 13 + 390 x 77 = 30,043 bytes.
func TestAChannelMapOverTheByteBudgetIsTooMany(t *testing.T) {
	f := newFixture(t, provider(true, news(), category("Big", 10000, "^CH ")))
	f.entries = []m3u.Entry{entry("CNN")}
	for i := range 390 {
		f.entries = append(f.entries, entry(fmt.Sprintf("CH %03d", i)))
	}

	f.reconcile()

	lp := f.plan()
	require.Len(t, lp.Categories, 2)
	assert.Equal(t, lineupCategory("Big", 10000, 390, true), lp.Categories[1])
	assert.False(t, lp.Categories[0].TooMany)
	for _, a := range lp.Channels {
		assert.Equal(t, "News", a.Category)
	}
	tooMany := 0
	for _, d := range lp.Dropped {
		if d.Reason == "tooMany" {
			tooMany++
		}
	}
	assert.Equal(t, 390, tooMany)
	got := f.channels()
	require.Len(t, got, 1, "nothing of the refused category is created")
	assert.Contains(t, got, keyOf("CNN"))
	ev := f.events()
	require.NotEmpty(t, ev)
	assert.True(t, strings.HasPrefix(ev[0], "Warning LineupCategoryTooMany "), "%v", ev)
	assert.Contains(t, ev[0], `"Big"`)
	assert.Contains(t, ev[0], "channel map is 30043 bytes, over the 30000 a DVR takes", "the byte budget, not the count, refused it")
}

// TestTheByteBudgetIsTheBoundary: 389 five-digit numbers (29,966 bytes)
// fit; 390 do not.
func TestTheByteBudgetIsTheBoundary(t *testing.T) {
	plan := func(n int) lineup.Plan {
		p := lineup.Plan{Categories: []lineup.Category{{Name: "Big", Base: 10000, Channels: n}}}
		for i := range n {
			p.Channels = append(p.Channels, lineup.Assignment{Key: fmt.Sprint(i), Category: "Big", Number: fmt.Sprint(10001 + i)})
		}
		return p
	}
	fits, overBytes := refuseOverBytes(plan(389))
	assert.Empty(t, overBytes)
	assert.False(t, fits.Categories[0].TooMany)
	assert.Len(t, fits.Channels, 389)
	assert.Empty(t, fits.Dropped)
	over, overBytes := refuseOverBytes(plan(390))
	assert.Equal(t, map[string]int{"Big": 30043}, overBytes)
	assert.True(t, over.Categories[0].TooMany)
	assert.Empty(t, over.Channels)
	assert.Len(t, over.Dropped, 390)
}

func lineupCategory(name string, base, channels int, tooMany bool) lineup.Category {
	return lineup.Category{Name: name, Base: base, Channels: channels, TooMany: tooMany}
}

// TestLongNamesAreClampedToTheirBounds: status.lineup's strings are cut to
// their CRD MaxLength in runes -- a rule label "<64-rune category>[10]" is
// 68, and a playlist name is unbounded -- so the status applies; a clamped
// status reads as unchanged on the next pass.
func TestLongNamesAreClampedToTheirBounds(t *testing.T) {
	long := strings.Repeat("é", 64)
	var cats []clustarrv1.LineupCategory
	for i := range 10 {
		cats = append(cats, category(fmt.Sprintf("R%d", i), int32(1000*(i+1)), "^NOMATCH$"))
	}
	cats = append(cats, category(long, 20000, "^LONG"))
	name := "LONG" + strings.Repeat("ü", 296)
	require.Equal(t, 300, utf8.RuneCountInString(name))
	f := newFixture(t, provider(true, cats...))
	f.entries = []m3u.Entry{entry(name)}

	f.reconcile()

	st := f.channels()[keyOf(name)].Status.Lineup
	require.NotNil(t, st, "the status applied")
	assert.Equal(t, "20001", st.Number)
	assert.Equal(t, long, st.Category, "a 64-rune category is within its bound")
	assert.Equal(t, long, st.Rule, "the label's [10] is cut at 64 runes")
	assert.Equal(t, 256, utf8.RuneCountInString(st.Name))
	assert.True(t, strings.HasPrefix(name, st.Name))
	assert.True(t, utf8.ValidString(st.Name))
	f.events()

	f.reconcile()
	assert.Empty(t, f.events(), "the clamped status reads as unchanged")
}

// TestARawSpecNameIsNoOverride: the ui's add posts the playlist's raw name
// as spec.name, so a spec.name equal to the entry's raw (or trimmed) name
// is no override and the channel sorts and shows by the trimmed name; a
// name of a person's own is kept.
func TestARawSpecNameIsNoOverride(t *testing.T) {
	p := provider(true, news())
	p.Spec.Lineup.Trim = []string{`^USA: `, `\s*\[1080p\]`}
	raw, intl, bbc := "USA: CNN [1080p]", "USA: CNN INTERNATIONAL [1080p]", "USA: BBC NEWS [1080p]"
	f := newFixture(t, p,
		channel(raw, "1", true, func(ch *clustarrv1.IPTVChannel) { ch.Spec.Name = raw }),
		channel(intl, "2", true, func(ch *clustarrv1.IPTVChannel) { ch.Spec.Name = "CNN International (my pick)" }),
		channel(bbc, "3", true, func(ch *clustarrv1.IPTVChannel) { ch.Spec.Name = "BBC NEWS" }),
	)
	f.entries = []m3u.Entry{entry(raw), entry(intl), entry(bbc)}

	f.reconcile()

	got := f.channels()
	assert.Equal(t, &clustarrv1.ChannelLineupStatus{Name: "BBC NEWS", Category: "News", Rule: "News[0]", Number: "1001"}, got[keyOf(bbc)].Status.Lineup)
	assert.Equal(t, &clustarrv1.ChannelLineupStatus{Name: "CNN", Category: "News", Rule: "News[0]", Number: "1002"}, got[keyOf(raw)].Status.Lineup,
		"the raw spec.name is not an override: trimmed, it sorts as CNN")
	assert.Equal(t, &clustarrv1.ChannelLineupStatus{Name: "CNN International (my pick)", Category: "News", Rule: "News[0]", Number: "1003"}, got[keyOf(intl)].Status.Lineup,
		"a person's own name is kept")
	assert.Equal(t, raw, got[keyOf(raw)].Spec.Name, "spec.name is never the lineup's to write")
}

// TestALostPlanObjectIsWrittenAgain: every pass writes the plan object, so
// one lost while the manager runs is back at the next pass, the plan
// unchanged.
func TestALostPlanObjectIsWrittenAgain(t *testing.T) {
	f := newFixture(t, provider(true, news()))
	f.entries = []m3u.Entry{entry("CNN")}
	f.reconcile()
	want := f.plan()
	require.NoError(t, f.store.Delete(context.Background(), livetvstore.LineupPlanObject(testKey)))

	f.reconcile()

	assert.Equal(t, want.Plan, f.plan().Plan)
}

// TestOnlyWhatHappenedIsCounted: a channel the cache does not show yet but
// the apiserver holds is read before the lineup would create it, so it is
// updated as existing (its ui label kept) and not counted as created; a
// delete that finds the channel gone counts nothing.
func TestOnlyWhatHappenedIsCounted(t *testing.T) {
	hidden := channel("CNN", "5", false)
	f := newFixtureWith(t, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if err := c.List(ctx, list, opts...); err != nil {
				return err
			}
			if l, ok := list.(*clustarrv1.IPTVChannelList); ok {
				l.Items = slices.DeleteFunc(l.Items, func(ch clustarrv1.IPTVChannel) bool { return ch.Name == hidden.Name })
			}
			return nil
		},
		Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
			return apierrors.NewNotFound(clustarrv1.GroupVersion.WithResource("iptvchannels").GroupResource(), "gone")
		},
	}, provider(true, news()), hidden)
	f.entries = []m3u.Entry{entry("CNN"), entry("BBC NEWS")}

	f.reconcile()

	got := f.channels()
	assert.Equal(t, clustarrv1.OriginUI, got[keyOf("CNN")].Labels[clustarrv1.LabelOrigin], "an existing channel the cache missed keeps its label")
	assert.Equal(t, "1002", got[keyOf("CNN")].Spec.Number)
	assert.True(t, got[keyOf("CNN")].Spec.Active)
	ev := f.events()
	require.Len(t, ev, 1)
	assert.Contains(t, ev[0], "1 created, 1 renumbered, 1 activated")

	// BBC leaves; seven days on its delete finds it gone: no deletion counted.
	f.entries = []m3u.Entry{entry("CNN")}
	f.reconcile()
	f.events()
	f.now = f.now.Add(DeleteAbsentAfter + time.Minute)
	f.reconcile()
	assert.Empty(t, f.events(), "a delete that found nothing is no change")
}
