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

package fetch_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	subtitleac "github.com/mediactl/clustarr/api/applyconfiguration/subtitle/subtitle/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/captionarr/providerset"
	"github.com/mediactl/clustarr/captionarr/status"
	"github.com/mediactl/clustarr/captionarr/worker/fetch"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// testCfg is the shared envtest control plane; nil when KUBEBUILDER_ASSETS
// is unset, in which case every envtest here SKIPS -- and a skip is not a
// pass.
var testCfg *rest.Config

func TestMain(m *testing.M) {
	code := func() int {
		if os.Getenv("KUBEBUILDER_ASSETS") == "" {
			return m.Run()
		}
		env := &envtest.Environment{
			CRDDirectoryPaths:     []string{"../../../config/crd/bases"},
			ErrorIfCRDPathMissing: true,
		}
		cfg, err := env.Start()
		if err != nil {
			panic("start envtest: " + err.Error())
		}
		testCfg = cfg
		defer func() {
			if err := env.Stop(); err != nil {
				panic("stop envtest: " + err.Error())
			}
		}()
		return m.Run()
	}()
	os.Exit(code)
}

// now is the fixed instant every fixture's worker clock reports. It is a
// real wall-clock time (not a far-past constant) because throttle windows
// recorded at it are compared against it, and the token bucket in
// captionarr/throttle reads time.Now for itself.
var now = time.Now().UTC().Truncate(time.Second)

const (
	mediaLogical = "/data/movies/Film (2010)/Film (2010).mkv"
	sidecarName  = "Film (2010).en.srt"
	releaseTitle = "Film.2010.1080p.BluRay.x264-GRP"
)

// srtWithHI is a two-cue SubRip file whose first cue is nothing but a sound
// cue, so the profile's removeHI mod visibly drops it.
const srtWithHI = "1\n00:00:01,000 --> 00:00:02,000\n[MUSIC PLAYING]\n\n2\n00:00:03,000 --> 00:00:04,000\nHello there.\n\n"

// fixture is one namespace with a video MediaFile on disk, its Movie, a
// SubtitleProfile wanting en and es, and the SubtitleRequest -- plus a
// worker wired to them.
type fixture struct {
	ctx     context.Context
	c       client.Client
	ns      string
	dataDir string
	profile string
	req     *subtitlev1alpha1.SubtitleRequest
	probe   string
	bus     *recordingBus
	source  *fakeSource
	worker  *fetch.Worker
}

var nsSeq atomic.Int32

func newFixture(t *testing.T, bus events.Bus) *fixture {
	t.Helper()
	if testCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	c, err := client.New(testCfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	ctx := t.Context()

	f := &fixture{ctx: ctx, c: c, dataDir: t.TempDir(), bus: &recordingBus{Bus: bus}, source: &fakeSource{}}
	f.ns = "fetch-" + strconv.Itoa(int(nsSeq.Add(1)))
	f.profile = f.ns
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: f.ns}}))

	require.NoError(t, c.Create(ctx, &subtitlev1alpha1.SubtitleProfile{
		ObjectMeta: metav1.ObjectMeta{Name: f.profile},
		Spec: subtitlev1alpha1.SubtitleProfileSpec{
			Languages: []subtitlev1alpha1.LanguageItem{
				{Key: "en", Language: "en", HI: subtitlev1alpha1.HIPolicyPrefer},
				{Key: "es", Language: "es", HI: subtitlev1alpha1.HIPolicyPrefer},
			},
			Mods: []subtitlev1alpha1.SubtitleMod{subtitlev1alpha1.SubtitleModRemoveHI},
		},
	}))
	t.Cleanup(func() {
		_ = c.Delete(context.Background(), &subtitlev1alpha1.SubtitleProfile{ObjectMeta: metav1.ObjectMeta{Name: f.profile}})
	})

	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "film", Namespace: f.ns},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 603, QualityProfileRef: "hd", RootFolderRef: "movies"},
	}))
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "film", Namespace: f.ns},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef:     commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "film"},
			Path:         mediaLogical,
			ImportedFrom: &catalogv1alpha1.ImportSource{ReleaseTitle: releaseTitle},
		},
	}))

	local := f.local(mediaLogical)
	require.NoError(t, os.MkdirAll(filepath.Dir(local), 0o755))
	require.NoError(t, os.WriteFile(local, []byte("not really a video"), 0o644))
	st, err := os.Stat(local)
	require.NoError(t, err)
	f.probe = mediainfo.ProbeHash(mediaLogical, st.Size(), st.ModTime())

	f.req = &subtitlev1alpha1.SubtitleRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "film", Namespace: f.ns},
		Spec:       subtitlev1alpha1.SubtitleRequestSpec{MediaFileRef: "film", ProfileRef: f.profile},
	}
	require.NoError(t, c.Create(ctx, f.req))

	f.worker = &fetch.Worker{
		Client: c, APIReader: c, Bus: f.bus, Providers: f.source, DataDir: f.dataDir,
		Clock: func() time.Time { return now },
	}
	f.seedLive(t, "en")
	return f
}

// schedule is the nextSearchAt seedLive gives every item it makes live.
var schedule = metav1.NewTime(now.Add(-time.Minute))

// seedLive makes langKeys live the way the item-liveness protocol does
// (status.IsLive): the controller schedules them. The worker writes a
// pending state first only because the CRD still requires items[].state;
// once F-4 relaxes that, the controller alone creates items (rule 2) and
// the first step is merely redundant.
func (f *fixture) seedLive(t *testing.T, langKeys ...string) {
	t.Helper()
	req := f.get(t)
	have := status.LiveItemKeys(req.Status)
	require.NoError(t, status.PatchRequest(f.ctx, f.c, k8s.ManagerCaptionarrWorker, req,
		func(ac *subtitleac.SubtitleRequestStatusApplyConfiguration) {
			for _, k := range langKeys {
				if !have.Has(k) {
					ac.Items = append(ac.Items, *subtitleac.SubtitleItem().WithLangKey(k).
						WithState(subtitlev1alpha1.SubtitleItemPending))
				}
			}
		}))

	req = f.get(t)
	req.Status.Items = slices.DeleteFunc(req.Status.Items, func(it subtitlev1alpha1.SubtitleItem) bool {
		return !status.IsLive(it) && !slices.Contains(langKeys, it.LangKey)
	})
	for i := range req.Status.Items {
		if !status.IsLive(req.Status.Items[i]) {
			req.Status.Items[i].NextSearchAt = &schedule
		}
	}
	require.NoError(t, status.PatchRequest(f.ctx, f.c, k8s.ManagerCaptionarr, req, nil))
	for _, k := range langKeys {
		require.True(t, status.LiveItemKeys(f.get(t).Status).Has(k), "setup: %s is not live", k)
	}
}

// withdraw is the controller no longer wanting langKey: its next apply
// simply omits the item (rule 1 of the protocol).
func (f *fixture) withdraw(t *testing.T, langKey string) {
	t.Helper()
	req := f.get(t)
	req.Status.Items = slices.DeleteFunc(req.Status.Items, func(it subtitlev1alpha1.SubtitleItem) bool {
		return it.LangKey == langKey || !status.IsLive(it)
	})
	require.NoError(t, status.PatchRequest(f.ctx, f.c, k8s.ManagerCaptionarr, req, nil))
}

func (f *fixture) local(logical string) string {
	return filepath.Join(f.dataDir, strings.TrimPrefix(logical, "/data"))
}

// get returns the live SubtitleRequest.
func (f *fixture) get(t *testing.T) *subtitlev1alpha1.SubtitleRequest {
	t.Helper()
	var r subtitlev1alpha1.SubtitleRequest
	require.NoError(t, f.c.Get(f.ctx, client.ObjectKeyFromObject(f.req), &r))
	return &r
}

// item returns the live item for langKey, failing when there is none.
func (f *fixture) item(t *testing.T, langKey string) subtitlev1alpha1.SubtitleItem {
	t.Helper()
	for _, it := range f.get(t).Status.Items {
		if it.LangKey == langKey {
			return it
		}
	}
	t.Fatalf("no item %q on the request", langKey)
	return subtitlev1alpha1.SubtitleItem{}
}

// message builds the FetchTask delivery the controller would publish.
func (f *fixture) message(t *testing.T, langKey string, mutate func(*schema.FetchTask)) *fakeMessage {
	t.Helper()
	live := f.get(t)
	ft := schema.FetchTask{
		RequestRef: schema.Ref{Namespace: f.ns, Name: live.Name, UID: string(live.UID)},
		LangKey:    langKey,
		ProbeHash:  f.probe,
	}
	if mutate != nil {
		mutate(&ft)
	}
	name, data, err := schema.Encode(ft)
	require.NoError(t, err)
	return &fakeMessage{env: &events.Envelope{
		Schema: name, Data: data, Type: "subtitle.FetchTask", Key: f.ns + "/" + live.Name,
	}}
}

// entry registers a remote-provider entry backed by p. Its UID is unique to
// the fixture so throttle state never leaks between tests.
func (f *fixture) entry(name string, typ subtitlev1alpha1.SubtitleProviderType, p *fakeProvider) providerset.Entry {
	e := providerset.Entry{
		Name: name, Namespace: f.ns, UID: f.ns + "-" + name, Type: typ,
		Priority: int32(len(f.source.entries) + 1), RateMilli: 1_000_000, Client: p,
	}
	f.source.entries = append(f.source.entries, e)
	return e
}

// fakeSource is a fixed provider set.
type fakeSource struct{ entries []providerset.Entry }

func (s *fakeSource) Build(context.Context, string) ([]providerset.Entry, error) {
	return s.entries, nil
}

// fakeProvider is a scripted subtitles.Provider.
type fakeProvider struct {
	name      string
	caps      subtitles.Capabilities
	cands     []subtitles.Candidate
	searchErr error
	files     map[string][]byte // FetchID -> raw subtitle
	dlErr     map[string]error  // FetchID -> download error
	onSearch  func()

	searches, downloads atomic.Int32
}

func newFakeProvider(name string) *fakeProvider {
	return &fakeProvider{
		name:  name,
		caps:  subtitles.Capabilities{Movies: true, Episodes: true, ForcedSearch: true, HashVerifiable: true},
		files: map[string][]byte{}, dlErr: map[string]error{},
	}
}

func (p *fakeProvider) Name() string                         { return p.name }
func (p *fakeProvider) Capabilities() subtitles.Capabilities { return p.caps }
func (p *fakeProvider) HIVerifiable() bool                   { return true }
func (p *fakeProvider) Search(context.Context, subtitles.Query) ([]subtitles.Candidate, error) {
	p.searches.Add(1)
	if p.onSearch != nil {
		p.onSearch()
	}
	return p.cands, p.searchErr
}

func (p *fakeProvider) Download(_ context.Context, c subtitles.Candidate) ([]byte, string, error) {
	p.downloads.Add(1)
	if err := p.dlErr[c.FetchID]; err != nil {
		return nil, "", err
	}
	return p.files[c.FetchID], c.FetchID + ".srt", nil
}

// candidate is an English candidate whose release_info is release.
func candidate(id, release string) subtitles.Candidate {
	return subtitles.Candidate{ID: id, FetchID: id, Language: "en", ReleaseInfo: release}
}

// fakeMessage is a minimal events.Message, mirroring
// importarr/worker/fileimport's.
type fakeMessage struct {
	env        *events.Envelope
	attempt    uint64
	inProgress atomic.Int32
}

func (m *fakeMessage) Envelope() *events.Envelope { return m.env }
func (m *fakeMessage) Subject() string            { return "" }
func (m *fakeMessage) Attempt() uint64 {
	if m.attempt == 0 {
		return 1
	}
	return m.attempt
}
func (m *fakeMessage) Ack(context.Context) error                { return nil }
func (m *fakeMessage) Nak(context.Context, time.Duration) error { return nil }
func (m *fakeMessage) Term(context.Context, string) error       { return nil }
func (m *fakeMessage) InProgress(context.Context) error {
	m.inProgress.Add(1)
	return nil
}

// recordingBus records every publish on top of a real bus.
type recordingBus struct {
	events.Bus
	mu        sync.Mutex
	published []published
}

type published struct {
	subject string
	env     *events.Envelope
}

func (b *recordingBus) Publish(ctx context.Context, subject string, e *events.Envelope, opts ...events.PublishOption) (events.Receipt, error) {
	b.mu.Lock()
	b.published = append(b.published, published{subject: subject, env: e.Clone()})
	b.mu.Unlock()
	return b.Bus.Publish(ctx, subject, e, opts...)
}

// subtitleEvents decodes every SubtitleEvent published so far.
func (b *recordingBus) subtitleEvents(t *testing.T) []schema.SubtitleEvent {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []schema.SubtitleEvent
	for _, p := range b.published {
		var e schema.SubtitleEvent
		require.NoError(t, schema.Decode(p.env.Schema, p.env.Data, &e))
		require.Equal(t, events.SubtitleEventSubject(e.Action, e.RequestRef.UID), p.subject)
		out = append(out, e)
	}
	return out
}

// memBus is an in-memory bus with the production topology.
func memBus(t *testing.T) events.Bus {
	t.Helper()
	b := membus.New(nil)
	require.NoError(t, b.Ensure(t.Context(), events.Default()))
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// natsBus is a real embedded JetStream server with the production topology,
// for every test that exercises the clustarr-provider-throttle KV: the
// in-memory bus has no key grammar and has let an illegal key ship twice.
// Mirrors captionarr/throttle's kvkey_contract_test.go.
func natsBus(t *testing.T) events.Bus {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "jetstream")
	require.NoError(t, err)
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-fetch", Host: "127.0.0.1", Port: -1,
		JetStream: true, StoreDir: dir, NoLog: true, NoSigs: true,
	})
	require.NoError(t, err)
	go srv.Start()
	if !srv.ReadyForConnections(20 * time.Second) {
		t.Fatal("embedded NATS server did not become ready")
	}
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	b, err := natsbus.New(nc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	require.NoError(t, b.Ensure(t.Context(), events.Default().ForSingleNode()))
	return b
}

// itemLeaves returns, per langKey, the status.items leaves mgr owns
// according to the apiserver's own managedFields -- the only place an
// over-claim shows, since pkg/k8s applies with ForceOwnership.
func itemLeaves(t *testing.T, req *subtitlev1alpha1.SubtitleRequest, mgr k8s.FieldManager) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, mf := range req.ManagedFields {
		if mf.Manager != string(mgr) || mf.Subresource != "status" || mf.FieldsV1 == nil {
			continue
		}
		var raw map[string]any
		require.NoError(t, json.Unmarshal(mf.FieldsV1.GetRawBytes(), &raw))
		st, _ := raw["f:status"].(map[string]any)
		items, _ := st["f:items"].(map[string]any)
		for key, v := range items {
			var k struct {
				LangKey string `json:"langKey"`
			}
			require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(key, "k:")), &k))
			for leaf := range v.(map[string]any) {
				if leaf != "." {
					out[k.LangKey] = append(out[k.LangKey], strings.TrimPrefix(leaf, "f:"))
				}
			}
			sort.Strings(out[k.LangKey])
		}
	}
	return out
}
