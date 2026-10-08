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

package importplan_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/import/importplan"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/app/import/worker/probe/probetest"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// The tests here decide imports from inspections the real import agent
// made: fileimport's inspect handler, over a fake cache holding the
// library's items, reads a payload written to disk, parses its names with
// the real release parser and freezes its fields, and its record is what
// importplan.Decide reads -- never an inspection shaped like the answer
// (CLAUDE.md). The execute handler places what an approved plan says.

const (
	ns = "media"
	// big is a video file over the 50 MiB sample floor; small is under it.
	big   = 60 << 20
	small = 10 << 20
)

// world is a library: its items in a fake cache, the bus the import
// records ride, and the import agent's worker.
type world struct {
	t      *testing.T
	dir    string
	c      client.Client
	bus    events.Bus
	w      *fileimport.Worker
	now    time.Time
	probes map[string]*commonv1.MediaInfo // by file name; absent probes fail
	seq    int
}

func profile(name string, kind catalogv1alpha1.ProfileMediaKind, cutoff string, tiers ...catalogv1alpha1.Tier) *catalogv1alpha1.QualityProfile {
	return &catalogv1alpha1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       catalogv1alpha1.QualityProfileSpec{MediaKind: kind, Cutoff: cutoff, Tiers: tiers},
	}
}

func tier(name string, qs ...string) catalogv1alpha1.Tier {
	return catalogv1alpha1.Tier{Name: name, Qualities: qs}
}

// lib is a root folder's path in the world.
func (wd *world) lib(kind string) string { return filepath.Join(wd.dir, "library", kind) }

func (wd *world) bin() string { return filepath.Join(wd.dir, "recycle") }

func movie(name, title string, year int32, profileRef, root, original string) *catalogv1alpha1.Movie {
	return &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(name + "-uid")},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: profileRef, RootFolderRef: root},
		Status: catalogv1alpha1.MovieStatus{Metadata: &catalogv1alpha1.MovieMetadata{
			Title: title, Year: year, OriginalLanguage: original,
		}},
	}
}

// newWorld is a library of movies, a series and an album.
func newWorld(t *testing.T) *world {
	t.Helper()
	wd := &world{t: t, dir: t.TempDir(), now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC), probes: map[string]*commonv1.MediaInfo{}}
	root := func(name string, kind catalogv1alpha1.RootFolderKind, path string) *catalogv1alpha1.RootFolder {
		require.NoError(t, os.MkdirAll(path, 0o755))
		return &catalogv1alpha1.RootFolder{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(name + "-uid")},
			Spec:       catalogv1alpha1.RootFolderSpec{Path: path, Kind: kind, RecycleBin: catalogv1alpha1.RecycleBin{Path: wd.bin()}},
		}
	}
	video := []catalogv1alpha1.Tier{
		tier("uhd", "Bluray-2160p"), tier("hd", "Bluray-1080p", "WEBDL-1080p"), tier("sd", "HDTV-720p"),
	}
	original := profile("original-audio", catalogv1alpha1.ProfileMediaKindVideo, "hd", video...)
	original.Spec.Language = "original"
	series := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "the-wire", Namespace: ns, UID: "the-wire-uid"},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 79126, QualityProfileRef: "hd", RootFolderRef: "tv"},
		Status: catalogv1alpha1.SeriesStatus{Metadata: &catalogv1alpha1.SeriesMetadata{
			Title: "The Wire", Year: 2002, OriginalLanguage: "en",
		}},
	}
	episode := func(n int32) *catalogv1alpha1.Episode {
		name := fmt.Sprintf("the-wire-s01e%02d", n)
		return &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(name + "-uid")},
			Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "the-wire", SeasonNumber: 1, EpisodeNumber: n},
			Status:     catalogv1alpha1.EpisodeStatus{Title: fmt.Sprintf("Episode %d", n)},
		}
	}
	artist := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "radiohead", Namespace: ns, UID: "radiohead-uid"},
		Spec:       catalogv1alpha1.ArtistSpec{QualityProfileRef: "lossless", RootFolderRef: "music"},
	}
	album := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "ok-computer", Namespace: ns, UID: "ok-computer-uid"},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", QualityProfileRef: ptr.To("lossless")},
		Status:     catalogv1alpha1.AlbumStatus{Path: filepath.Join(wd.lib("music"), "Radiohead", "OK Computer (1997)")},
	}
	objs := []client.Object{
		profile("hd", catalogv1alpha1.ProfileMediaKindVideo, "hd", video...),
		profile("uhd-only", catalogv1alpha1.ProfileMediaKindVideo, "uhd", tier("uhd", "Bluray-2160p")),
		original,
		profile("lossless", catalogv1alpha1.ProfileMediaKindMusic, "lossless", tier("lossless", "FLAC")),
		root("movies", catalogv1alpha1.RootFolderKindMovie, wd.lib("movies")),
		root("tv", catalogv1alpha1.RootFolderKindSeries, wd.lib("tv")),
		root("music", catalogv1alpha1.RootFolderKindMusic, wd.lib("music")),
		movie("heat", "Heat", 1995, "hd", "movies", "en"),
		movie("ronin", "Ronin", 1998, "hd", "movies", "en"),
		movie("shoplifters", "Shoplifters", 2018, "original-audio", "movies", "ja"),
		series, episode(1), episode(2), artist, album,
	}
	wd.c = fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithObjects(objs...).
		WithIndex(&catalogv1alpha1.MediaFile{}, mfindex.Item, mfindex.Extract).
		Build()

	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(context.Background(), events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	wd.bus = bus
	wd.w = fileimport.NewWorker(wd.c, bus, probetest.ProberFunc(func(_ context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
		if mi, ok := wd.probes[filepath.Base(path)]; ok {
			return mi.DeepCopy(), nil, nil
		}
		return nil, nil, errors.New("no probe of this file in the world")
	}))
	wd.w.Clock = func() time.Time { return wd.now }
	return wd
}

// payload writes files (path relative to the content root -> size) under a
// fresh content root and returns it.
func (wd *world) payload(files map[string]int64) string {
	wd.t.Helper()
	wd.seq++
	root := filepath.Join(wd.dir, "downloads", "grab-"+string(rune('a'+wd.seq)))
	for rel, size := range files {
		p := filepath.Join(root, rel)
		require.NoError(wd.t, os.MkdirAll(filepath.Dir(p), 0o755))
		f, err := os.Create(p) //nolint:gosec // under t.TempDir()
		require.NoError(wd.t, err)
		require.NoError(wd.t, f.Truncate(size))
		require.NoError(wd.t, f.Close())
	}
	return root
}

// mediaFile is an existing MediaFile of item at path with q, written to disk
// as well.
func (wd *world) mediaFile(name string, kind commonv1.MediaKind, item, path, q string) *catalogv1alpha1.MediaFile {
	wd.t.Helper()
	require.NoError(wd.t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(wd.t, os.WriteFile(path, []byte(name), 0o644))
	pk := "video"
	if kind == commonv1.MediaKindAlbum {
		pk = "music"
	}
	def, ok := quality.Lookup(pk, q)
	require.True(wd.t, ok, q)
	return &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(name + "-uid")},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: kind, Name: item}, Path: path, Quality: def.Quality,
		},
	}
}

// add creates objs in the world's cache.
func (wd *world) add(objs ...client.Object) {
	wd.t.Helper()
	for _, o := range objs {
		require.NoError(wd.t, wd.c.Create(context.Background(), o))
	}
}

// grab is one Completed entry's import, driven as the manager's downloads
// stage drives it.
type grab struct {
	wd  *world
	in  importplan.Input
	dec lifecycle.ImportDecision
}

// grab is an automatic grab of title for the item kind/name, whose payload
// is contentRoot; Targets are the item, or the series' episodes.
func (wd *world) grab(kind commonv1.MediaKind, name, title, contentRoot string) *grab {
	wd.t.Helper()
	ctx := context.Background()
	var (
		obj      client.Object
		profRef  string
		original string
	)
	switch kind {
	case commonv1.MediaKindMovie:
		var m catalogv1alpha1.Movie
		require.NoError(wd.t, wd.c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &m))
		obj, profRef, original = &m, m.Spec.QualityProfileRef, m.Status.Metadata.OriginalLanguage
	case commonv1.MediaKindSeries:
		var s catalogv1alpha1.Series
		require.NoError(wd.t, wd.c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &s))
		obj, profRef, original = &s, s.Spec.QualityProfileRef, s.Status.Metadata.OriginalLanguage
	case commonv1.MediaKindAlbum:
		var a catalogv1alpha1.Album
		require.NoError(wd.t, wd.c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &a))
		obj, profRef = &a, ptr.Deref(a.Spec.QualityProfileRef, "")
	default:
		obj = &catalogv1alpha1.Artist{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID(name + "-uid")}}
	}
	owner := schema.ItemRef{
		Kind: strings.ToUpper(string(kind)[:1]) + string(kind)[1:],
		Ref:  schema.Ref{Namespace: ns, Name: obj.GetName(), UID: string(obj.GetUID())},
	}
	targets := []schema.ItemRef{owner}
	if kind == commonv1.MediaKindSeries {
		targets = nil
		var eps catalogv1alpha1.EpisodeList
		require.NoError(wd.t, wd.c.List(ctx, &eps, client.InNamespace(ns)))
		for _, e := range eps.Items {
			targets = append(targets, schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Namespace: ns, Name: e.Name, UID: string(e.UID)}})
		}
	}
	wd.seq++
	id := name + "-" + string(rune('a'+wd.seq))
	g := &grab{wd: wd, in: importplan.Input{
		Owner: owner, OwnerKind: kind,
		Entry: catalogv1alpha1.DownloadEntry{
			ID: "dl-" + id, UID: "entry-" + id,
			Release:   catalogv1alpha1.DownloadRelease{Title: title, IndexerName: "nzbgeek", Protocol: commonv1.ProtocolTorrent},
			GrabbedBy: commonv1.GrabSourceSearch, GrabbedAt: metav1.NewTime(wd.now.Add(-time.Hour)),
			Phase: commonv1.DownloadPhaseCompleted,
		},
		Transfer:         &schema.TransferRecord{ContentRoot: contentRoot},
		Targets:          targets,
		OriginalLanguage: original,
		Now:              wd.now,
	}}
	g.useProfile(profRef)
	return g
}

// useProfile resolves ref as the stage does: Profile nil with ProfileError
// when it cannot be.
func (g *grab) useProfile(ref string) {
	g.wd.t.Helper()
	g.in.ProfileRef, g.in.Profile, g.in.ProfileError = ref, nil, ""
	var qp catalogv1alpha1.QualityProfile
	if err := g.wd.c.Get(context.Background(), client.ObjectKey{Name: ref}, &qp); err != nil {
		g.in.ProfileError = "it does not exist"
		return
	}
	p, errs := quality.FromCRD(&qp, catalogue.LoadedCatalogue())
	require.Empty(g.wd.t, errs)
	g.in.Profile = &p
}

// record is the entry's record of phase, nil when absent.
func (g *grab) record(phase string) *schema.ImportRecord {
	g.wd.t.Helper()
	rd := records.NewReader(g.wd.bus.KV(events.BucketImports), agentrecords.Imports())
	rec, _, ok, err := rd.Get(context.Background(), agentrecords.ImportKey(g.in.Entry.UID, phase))
	require.NoError(g.wd.t, err)
	if !ok {
		return nil
	}
	return rec
}

// decide is the stage's next decision: the records, the covered items'
// MediaFiles by mfindex's item keys, the MediaFiles the execute named, and
// the last summary.
func (g *grab) decide(mutate ...func(*importplan.Input)) lifecycle.ImportDecision {
	g.wd.t.Helper()
	ctx := context.Background()
	in := g.in
	in.Now = g.wd.now
	if g.dec.Summary.Phase != "" {
		s := *g.dec.Summary.DeepCopy()
		in.Entry.Import = &s
	}
	in.Inspect = g.record(schema.ImportSubInspect)
	in.Execute = g.record(schema.ImportSubExecute)
	in.Existing = map[string][]catalogv1alpha1.MediaFile{}
	in.Named = map[string]*catalogv1alpha1.MediaFile{}
	if r := in.Inspect; r != nil && r.Inspect != nil {
		for _, f := range r.Inspect.Files {
			if f.Proposed == nil {
				continue
			}
			keys := f.Keys
			if len(keys) == 0 {
				keys = []string{f.Proposed.Name}
			}
			for _, k := range keys {
				kind := importplan.MediaKindOf(f.Proposed.Kind)
				files, err := mfindex.FilesOf(ctx, g.wd.c, ns, kind, k)
				require.NoError(g.wd.t, err)
				in.Existing[importplan.ItemKey(kind, k)] = files
			}
		}
	}
	for _, m := range mutate {
		m(&in)
	}
	g.dec = importplan.Decide(in)
	return g.dec
}

// deliver hands the last decision's task to the import agent.
func (g *grab) deliver() {
	g.wd.t.Helper()
	require.NotNil(g.wd.t, g.dec.Dispatch, "no task is owed: %+v", g.dec.Summary)
	task, ok := g.dec.Dispatch.Task.(schema.Payload)
	require.True(g.wd.t, ok)
	name, data, err := schema.Encode(task)
	require.NoError(g.wd.t, err)
	require.NoError(g.wd.t, g.wd.w.Handle(context.Background(), &message{env: &events.Envelope{Schema: name, Data: data}}))
}

// inspected issues the inspect, has the agent answer it, and returns the
// decision over its record: an approved plan's execute task, or a verdict.
func (g *grab) inspected(mutate ...func(*importplan.Input)) lifecycle.ImportDecision {
	g.wd.t.Helper()
	dec := g.decide(mutate...)
	require.NotNil(g.wd.t, dec.Dispatch, "%+v", dec.Summary)
	require.Equal(g.wd.t, schema.ImportSubInspect, dec.Dispatch.Phase, "%+v", dec.Summary)
	g.deliver()
	return g.decide(mutate...)
}

// plan is the approved plan's execute task.
func (g *grab) plan() *schema.ImportExecuteTask {
	g.wd.t.Helper()
	require.NotNil(g.wd.t, g.dec.Dispatch, "the import was not approved: %+v", g.dec.Summary)
	require.Equal(g.wd.t, schema.ImportSubExecute, g.dec.Dispatch.Phase, "%+v", g.dec.Summary)
	task, ok := g.dec.Dispatch.Task.(*schema.ImportExecuteTask)
	require.True(g.wd.t, ok)
	return task
}

// executed has the agent carry out the approved plan and returns the
// decision over its record.
func (g *grab) executed(mutate ...func(*importplan.Input)) lifecycle.ImportDecision {
	g.wd.t.Helper()
	g.plan()
	g.deliver()
	return g.decide(mutate...)
}

// message is a delivery of one task.
type message struct{ env *events.Envelope }

func (m *message) Envelope() *events.Envelope               { return m.env }
func (m *message) Subject() string                          { return "" }
func (m *message) Attempt() uint64                          { return 1 }
func (m *message) Ack(context.Context) error                { return nil }
func (m *message) Nak(context.Context, time.Duration) error { return nil }
func (m *message) Term(context.Context, string) error       { return nil }
func (m *message) InProgress(context.Context) error         { return nil }
