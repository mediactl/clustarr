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

package subtitlerequest_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/controller/subtitlerequest"
	"github.com/mediactl/clustarr/app/caption/datapath"
	"github.com/mediactl/clustarr/app/caption/status"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// One apiserver for the package: every test works in its own namespace and
// names its own cluster-scoped SubtitleProfile, and every request names its
// profile explicitly, so no test's default profile can be selected by
// another's.
var (
	testCfg    *rest.Config
	testClient client.Client
)

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run()) // the envtest suites skip themselves
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "envtest:", err)
		os.Exit(1)
	}
	testCfg = cfg
	testClient, err = client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		fmt.Fprintln(os.Stderr, "client:", err)
		_ = env.Stop()
		os.Exit(1)
	}
	code := m.Run()
	_ = env.Stop()
	os.Exit(code)
}

// testStart is whole seconds: metav1.Time serialises to the second, so a
// sub-second clock would make every round-tripped timestamp compare unequal.
var testStart = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

const probeHash1 = "hash-1"

// published is one Publish call the reconciler made, receipt included.
type published struct {
	subject string
	msgID   string
	env     *events.Envelope
	task    schema.FetchTask
	dup     bool
}

// recordingBus forwards to a real membus -- so the work stream's real
// one-hour deduplication window decides Duplicate, not a stub -- and records
// every call.
type recordingBus struct {
	inner events.Publisher
	mu    sync.Mutex
	got   []published
}

func (b *recordingBus) Publish(ctx context.Context, subject string, e *events.Envelope, opts ...events.PublishOption) (events.Receipt, error) {
	rcpt, err := b.inner.Publish(ctx, subject, e, opts...)
	if err != nil {
		return rcpt, err
	}
	var ft schema.FetchTask
	if err := json.Unmarshal(e.Data, &ft); err != nil {
		return rcpt, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.got = append(b.got, published{
		subject: subject, msgID: events.ResolvePublishOptions(e, opts).MsgID,
		env: e.Clone(), task: ft, dup: rcpt.Duplicate,
	})
	return rcpt, nil
}

func (b *recordingBus) calls() []published {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]published(nil), b.got...)
}

// stored is the calls the stream accepted as new messages.
func (b *recordingBus) stored() []published {
	var out []published
	for _, p := range b.calls() {
		if !p.dup {
			out = append(out, p)
		}
	}
	return out
}

type fixture struct {
	t     *testing.T
	ctx   context.Context
	c     client.Client
	ns    string
	dir   string
	clock *clockwork.FakeClock
	bus   *recordingBus
	r     *subtitlerequest.Reconciler
}

func newFixture(t *testing.T, ns string) *fixture {
	t.Helper()
	if testClient == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	ctx := context.Background()
	require.NoError(t, client.IgnoreAlreadyExists(testClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))

	clock := clockwork.NewFakeClockAt(testStart)
	mb := membus.New(clock)
	require.NoError(t, mb.Ensure(ctx, events.Default().ForSingleNode()))
	t.Cleanup(func() { _ = mb.Close() })
	bus := &recordingBus{inner: mb}
	// dir stands in for the media volume: it is the reconciler's --data-dir,
	// and every MediaFile path is the logical /data path that maps into it,
	// exactly as in a Deployment. A path read literally would not exist.
	dir := t.TempDir()

	return &fixture{
		t: t, ctx: ctx, c: testClient, ns: ns, dir: dir, clock: clock, bus: bus,
		r: &subtitlerequest.Reconciler{Client: testClient, Bus: bus, Now: clock.Now, DataDir: dir},
	}
}

func lang(key, language string) subtitlev1alpha1.LanguageItem {
	return subtitlev1alpha1.LanguageItem{Key: key, Language: language}
}

// profile creates a cluster-scoped SubtitleProfile named after the test's
// namespace; the apiserver fills every CRD default.
func (f *fixture) profile(mutate func(*subtitlev1alpha1.SubtitleProfileSpec)) *subtitlev1alpha1.SubtitleProfile {
	f.t.Helper()
	p := &subtitlev1alpha1.SubtitleProfile{
		ObjectMeta: metav1.ObjectMeta{Name: f.ns},
		Spec: subtitlev1alpha1.SubtitleProfileSpec{
			Languages: []subtitlev1alpha1.LanguageItem{lang("en", "en"), lang("de", "de")},
			Cutoff:    ptr.To("de"),
		},
	}
	if mutate != nil {
		mutate(&p.Spec)
	}
	require.NoError(f.t, f.c.Create(f.ctx, p))
	f.t.Cleanup(func() { _ = f.c.Delete(context.Background(), p) })
	return p
}

// englishTrack is how ffprobe really tags an English subtitle: ISO 639-2.
func englishTrack() *commonv1alpha1.MediaInfo {
	return &commonv1alpha1.MediaInfo{
		Container: "mkv",
		Audio:     []commonv1alpha1.AudioStream{{Index: 1, Codec: "aac", Language: "eng"}},
		Subtitles: []commonv1alpha1.SubtitleStream{{Index: 2, Codec: "subrip", Language: "eng"}},
	}
}

// movie creates the Movie a MediaFile of the same name belongs to: a
// MediaFile whose item is gone is one Clustarr no longer manages, and its
// request is Blocked (ItemNotFound).
func (f *fixture) movie(name string) *catalogv1alpha1.Movie {
	f.t.Helper()
	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 603, QualityProfileRef: "hd", RootFolderRef: "movies"},
	}
	require.NoError(f.t, f.c.Create(f.ctx, m))
	return m
}

// mediaFile writes <name>.mkv plus any sidecars into the fixture's directory
// and creates the MediaFile -- at the logical /data/<name>.mkv, which the
// reconciler's DataDir maps onto that directory -- and its Movie, probed (as
// catalogarr would) when mi is non-nil.
func (f *fixture) mediaFile(name string, mi *commonv1alpha1.MediaInfo, sidecars ...string) *catalogv1alpha1.MediaFile {
	f.t.Helper()
	for _, s := range append([]string{name + ".mkv"}, sidecars...) {
		require.NoError(f.t, os.WriteFile(filepath.Join(f.dir, s), []byte("x"), 0o600))
	}
	f.movie(name)
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef:  commonv1alpha1.MediaRef{Kind: commonv1alpha1.MediaKindMovie, Name: name},
			Path:      filepath.Join(datapath.Root, name+".mkv"),
			SizeBytes: 4 << 30,
			ModTime:   metav1.NewTime(testStart.Add(-48 * time.Hour)),
		},
	}
	require.NoError(f.t, f.c.Create(f.ctx, mf))
	if mi != nil {
		f.probe(name, probeHash1, *mi)
	}
	return mf
}

// probe stands in for catalogarr's probe write.
func (f *fixture) probe(name, hash string, mi commonv1alpha1.MediaInfo) {
	f.t.Helper()
	_, err := k8s.PatchStatus(f.ctx, f.c, k8s.ManagerCatalogarr,
		catalogac.MediaFile(name, f.ns).WithStatus(catalogac.MediaFileStatus().WithProbeHash(hash).WithMediaInfo(mi)))
	require.NoError(f.t, err)
}

func (f *fixture) request(name string) *subtitlev1alpha1.SubtitleRequest {
	f.t.Helper()
	sr := &subtitlev1alpha1.SubtitleRequest{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns},
		Spec:       subtitlev1alpha1.SubtitleRequestSpec{MediaFileRef: name, ProfileRef: f.ns},
	}
	require.NoError(f.t, f.c.Create(f.ctx, sr))
	return sr
}

// forceSearch sets spec.forceSearch as a user (or the UI) would, and returns
// the generation the edit produced.
func (f *fixture) forceSearch(name string) int64 {
	f.t.Helper()
	sr := f.get(name)
	patch := client.MergeFrom(sr.DeepCopy())
	sr.Spec.ForceSearch = true
	require.NoError(f.t, f.c.Patch(f.ctx, sr, patch))
	return sr.Generation
}

func (f *fixture) reconcile(name string) ctrl.Result {
	f.t.Helper()
	res, err := f.r.Reconcile(f.ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: f.ns, Name: name}})
	require.NoError(f.t, err)
	return res
}

func (f *fixture) get(name string) *subtitlev1alpha1.SubtitleRequest {
	f.t.Helper()
	var sr subtitlev1alpha1.SubtitleRequest
	require.NoError(f.t, f.c.Get(f.ctx, types.NamespacedName{Namespace: f.ns, Name: name}, &sr))
	return &sr
}

// report stands in for the F-5 fetch worker reporting on one language: a
// fresh read, the edit applied to that language's item -- which the
// controller must already have created; the worker never creates one -- and
// the worker's apply.
func (f *fixture) report(name, langKey string, edit func(*subtitlev1alpha1.SubtitleItem)) {
	f.t.Helper()
	sr := f.get(name)
	found := false
	for i := range sr.Status.Items {
		if sr.Status.Items[i].LangKey == langKey {
			edit(&sr.Status.Items[i])
			found = true
		}
	}
	require.Truef(f.t, found, "the controller has not created an item for %q", langKey)
	f.workerApplyFrom(sr)
}

// workerApply is one more worker apply from a fresh read, changing nothing.
func (f *fixture) workerApply(name string) {
	f.t.Helper()
	f.workerApplyFrom(f.get(name))
}

// workerApplyFrom is the worker's real apply path from snapshot sr:
// app/caption/status.PatchRequest under k8s.ManagerCaptionarrWorker, whose
// RequestWorkerFields re-sends leaves only for items live IN sr (rule 3 of
// the liveness protocol). Nothing is filtered here, so the protocol under
// test is the worker's own. A stale sr is how a test makes the worker race
// the controller.
func (f *fixture) workerApplyFrom(sr *subtitlev1alpha1.SubtitleRequest) {
	f.t.Helper()
	require.NoError(f.t, status.PatchRequest(f.ctx, f.c, k8s.ManagerCaptionarrWorker, sr.DeepCopy(), nil))
}

// sidecar writes a subtitle file next to the fixture's video, as the worker
// does before it reports a download.
func (f *fixture) sidecar(name string) {
	f.t.Helper()
	require.NoError(f.t, os.WriteFile(filepath.Join(f.dir, name), []byte("1\n00:00:01,000 --> 00:00:02,000\nx\n"), 0o600))
}

func item(t *testing.T, sr *subtitlev1alpha1.SubtitleRequest, langKey string) subtitlev1alpha1.SubtitleItem {
	t.Helper()
	for _, it := range sr.Status.Items {
		if it.LangKey == langKey {
			return it
		}
	}
	t.Fatalf("no item %q in %s/%s", langKey, sr.Namespace, sr.Name)
	return subtitlev1alpha1.SubtitleItem{}
}

func cond(sr *subtitlev1alpha1.SubtitleRequest, t string) metav1.Condition {
	if c := k8s.FindCondition(sr.Status.Conditions, t); c != nil {
		return *c
	}
	return metav1.Condition{}
}

// assertManagedFieldsSplit holds the object's managedFields to the R4 split,
// leaf for leaf. It is the only assertion that can see an over-claim:
// pkg/k8s forces ownership, so a manager that claimed another's leaf would
// take it with every value on the object unchanged.
//
// wantItems maps each manager to the per-item leaves it must own, exactly.
func assertManagedFieldsSplit(t *testing.T, sr *subtitlev1alpha1.SubtitleRequest,
	wantTop map[k8s.FieldManager][]string, wantItems map[k8s.FieldManager]map[string][]string,
) {
	t.Helper()
	seen := map[k8s.FieldManager]bool{}
	for _, e := range sr.ManagedFields {
		mgr := k8s.FieldManager(e.Manager)
		if e.Subresource != "status" || e.FieldsV1 == nil {
			continue
		}
		want, ok := wantTop[mgr]
		if !ok {
			if mgr == k8s.ManagerCaptionarr || mgr == k8s.ManagerCaptionarrWorker {
				t.Errorf("%s owns status fields the test expects it not to own at all", mgr)
			}
			continue
		}
		seen[mgr] = true
		var raw map[string]any
		require.NoError(t, json.Unmarshal(e.FieldsV1.GetRawBytes(), &raw))
		st, ok := raw["f:status"].(map[string]any)
		require.Truef(t, ok, "%s: no f:status", mgr)

		var top []string
		for k := range st {
			top = append(top, strings.TrimPrefix(k, "f:"))
		}
		sort.Strings(top)
		wantSorted := append([]string(nil), want...)
		sort.Strings(wantSorted)
		assert.Equalf(t, wantSorted, top, "%s's top-level status fields", mgr)

		gotItems := map[string][]string{}
		if items, ok := st["f:items"].(map[string]any); ok {
			for key, v := range items {
				var k struct {
					LangKey string `json:"langKey"`
				}
				require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(key, "k:")), &k))
				var leaves []string
				for leaf := range v.(map[string]any) {
					if leaf != "." {
						leaves = append(leaves, strings.TrimPrefix(leaf, "f:"))
					}
				}
				sort.Strings(leaves)
				gotItems[k.LangKey] = leaves
			}
		}
		wantItemLeaves := map[string][]string{}
		for lk, leaves := range wantItems[mgr] {
			s := append([]string(nil), leaves...)
			sort.Strings(s)
			wantItemLeaves[lk] = s
		}
		assert.Equalf(t, wantItemLeaves, gotItems, "%s's per-item leaves", mgr)
	}
	for mgr := range wantTop {
		assert.Truef(t, seen[mgr], "%s owns nothing on status", mgr)
	}
}
