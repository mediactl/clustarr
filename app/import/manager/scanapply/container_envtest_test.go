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

package scanapply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrl "sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/controller/importexclusion"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/app/import/scanprogress"
	"github.com/mediactl/clustarr/app/intake"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/names"
)

// envtestClient talks straight to a real apiserver with the CRDs
// installed, nil without KUBEBUILDER_ASSETS. The package's other tests run
// the applier against controller-runtime's fake client, which validates
// nothing and applies no CRD default, so a create the CRD would refuse or
// default -- an Author's monitorNewItems enum, an Artist's metadata
// profile, an Audiobook's region -- is proven only here.
var envtestClient client.Client

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run()) // the envtests skip themselves
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start envtest: %v\n", err)
		os.Exit(1)
	}
	code := func() int {
		envtestClient, err = client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
		if err != nil {
			fmt.Fprintf(os.Stderr, "build client: %v\n", err)
			return 1
		}
		return m.Run()
	}()
	if err := env.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stop envtest: %v\n", err)
	}
	os.Exit(code)
}

// scanEnv is one test's namespace, bus and applier against the apiserver.
type scanEnv struct {
	t   *testing.T
	ctx context.Context
	c   client.Client
	ns  string
	bus events.Bus
	a   *Applier
	now time.Time
}

func newScanEnv(t *testing.T, ns string) *scanEnv {
	t.Helper()
	if envtestClient == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	ctx := context.Background()
	c := envtestClient
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	now := time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC)
	return &scanEnv{
		t: t, ctx: ctx, c: c, ns: ns, bus: bus, now: now,
		a: &Applier{Client: c, Reader: c, APIReader: c, Bus: bus, Now: func() time.Time { return now }},
	}
}

// root creates a RootFolder of kind under /data/media/<name> with defaults.
// No directory is made: the applier touches no file for a present
// observation, and the path's CEL rule wants /data/media/.
func (e *scanEnv) root(name string, kind catalogv1alpha1.RootFolderKind, d catalogv1alpha1.RootDefaults) *catalogv1alpha1.RootFolder {
	e.t.Helper()
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Namespace: e.ns, Name: name},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: "/data/media/" + name, Kind: kind, Defaults: d},
	}
	require.NoError(e.t, e.c.Create(e.ctx, rf))
	return rf
}

// scan is the LibraryScan the observations name; only its uid keys the
// applier's tally, so no object is created.
func (e *scanEnv) scan() schema.Ref {
	return schema.Ref{Namespace: e.ns, Name: "scan", UID: e.ns + "-scan-uid"}
}

// observe hands obs to the intake's scan consumer as the rescan agent
// publishes it (schema.Encode in an envelope, decoded by Handle), so the
// proposal crosses the wire format, and the consumer's own verdict --
// applied, retried or dead-lettered -- is what the test reads.
func (e *scanEnv) observe(root *catalogv1alpha1.RootFolder, rel string, at *schema.ScanAttribution, frozen *schema.FrozenFields) error {
	e.t.Helper()
	obs := schema.ScanObservation{
		Scan: e.scan(), RootFolder: schema.Ref{Namespace: e.ns, Name: root.Name, UID: string(root.UID)},
		Kind: schema.ScanObservationPresent, Path: root.Spec.Path + "/" + rel, SizeBytes: 4 << 20, ModTime: e.now,
		Attribution: at, Frozen: frozen, SeenAt: e.now,
	}
	name, data, err := schema.Encode(obs)
	require.NoError(e.t, err)
	env := &events.Envelope{ID: obs.Path, Type: "import.ScanObservation", Schema: name, Key: e.ns + "/scan", Time: e.now, Data: data}
	return (&intake.ScanConsumer{Applier: e.a}).Handle(e.ctx, delivery{env: env})
}

// tally is the scan's applied tally, zero when none was written.
func (e *scanEnv) tally() scanprogress.Progress {
	e.t.Helper()
	entry, err := e.bus.KV(events.BucketProgress).Get(e.ctx, scanprogress.AppliedKey(e.scan().UID))
	if errors.Is(err, events.ErrKeyNotFound) {
		return scanprogress.Progress{}
	}
	require.NoError(e.t, err)
	p, err := scanprogress.DecodeProgress(entry.Value)
	require.NoError(e.t, err)
	return p
}

func (e *scanEnv) mediaFiles() []catalogv1alpha1.MediaFile {
	e.t.Helper()
	var list catalogv1alpha1.MediaFileList
	require.NoError(e.t, e.c.List(e.ctx, &list, client.InNamespace(e.ns)))
	return list.Items
}

// delivery is one JetStream delivery of an observation, as the intake's
// handler sees it.
type delivery struct{ env *events.Envelope }

func (d delivery) Ack(context.Context) error                { return nil }
func (d delivery) Nak(context.Context, time.Duration) error { return nil }
func (d delivery) Term(context.Context, string) error       { return nil }
func (d delivery) InProgress(context.Context) error         { return nil }
func (d delivery) Envelope() *events.Envelope               { return d.env }
func (d delivery) Subject() string                          { return "clustarr.intake.scan" }
func (d delivery) Attempt() uint64                          { return 1 }

// The MusicBrainz artist id of Nirvana, as the plan's identify step
// (W7.1) resolves an artist folder: a token, agreeing tags or the strict
// search, then one present observation per folder.
const nirvanaMBID = "5b11f4ce-a62d-471e-81fc-a69a8278c7da"

// artistAt is the attribution the walk proposes for the artist folder
// "Nirvana": the create, its folder pinned, the hit's title, no frozen
// fields (2026-11-10 plan W7.1).
func artistAt(root string) *schema.ScanAttribution {
	return &schema.ScanAttribution{
		Kind: string(commonv1.MediaKindArtist), ProviderID: "musicbrainz:" + nirvanaMBID, Evidence: "folder",
		Create: &schema.ItemCreate{
			Kind: string(commonv1.MediaKindArtist), MusicBrainzID: nirvanaMBID, Title: "Nirvana",
			RootFolderRef: root, Folder: "Nirvana",
		},
	}
}

// A root scan creates the Artist its folder names (the 2026-11-10 non-video
// libraries design's D16; plan W7.3): named by names.Artist, pinned to the
// folder its files are in, with the root's default profile, monitored flag,
// monitorNewItems and tags, every album monitored and no search on add, its
// metadata profile defaulted by the apiserver (album and ep, studio,
// official: an Artist without them lists no album), written under
// import-worker; the observation records no MediaFile, since the file
// awaits the artist's albums. A second observation of the same folder
// finds it: no duplicate, nothing counted created twice.
func TestAScanCreatesTheArtistItsFolderNamesOnce(t *testing.T) {
	e := newScanEnv(t, "scanapply-artist")
	music := e.root("music", catalogv1alpha1.RootFolderKindMusic, catalogv1alpha1.RootDefaults{
		QualityProfileRef: "lossless", Monitored: ptr.To(false),
		MonitorNewItems: catalogv1alpha1.MonitorNewItemsNew, Tags: []string{"scanned"},
	})

	require.NoError(t, e.observe(music, "Nirvana/01 - Smells Like Teen Spirit.flac", artistAt(music.Name), nil))

	name := names.Artist("Nirvana", nirvanaMBID)
	var ar catalogv1alpha1.Artist
	require.NoError(t, e.c.Get(e.ctx, types.NamespacedName{Namespace: e.ns, Name: name}, &ar), "the scan created no Artist")
	assert.Equal(t, nirvanaMBID, ar.Spec.MusicBrainzID)
	assert.Equal(t, ptr.To("Nirvana"), ar.Spec.Folder, "the artist is pinned to the folder its files are in")
	assert.Equal(t, "lossless", ar.Spec.QualityProfileRef)
	assert.Equal(t, music.Name, ar.Spec.RootFolderRef)
	assert.Equal(t, ptr.To(false), ar.Spec.Monitored, "the root's defaults.monitored")
	assert.Equal(t, catalogv1alpha1.MonitorNewItemsNew, ar.Spec.MonitorNewItems, "the root's defaults.monitorNewItems")
	assert.Equal(t, []string{"scanned"}, ar.Spec.Tags, "the root's defaults.tags")
	assert.Equal(t, catalogv1alpha1.ArtistMonitorAll, ar.Spec.AddOptions.Monitor)
	assert.Equal(t, ptr.To(false), ar.Spec.AddOptions.SearchForMissing, "a scan never starts a search on add")
	assert.Equal(t, []string{"album", "ep"}, ar.Spec.MetadataProfile.PrimaryTypes, "the metadata profile's defaults")
	assert.Equal(t, []string{"studio"}, ar.Spec.MetadataProfile.SecondaryTypes)
	assert.Equal(t, []string{"official"}, ar.Spec.MetadataProfile.ReleaseStatuses)
	assert.True(t, ownsField(&ar, mediafilespec.FieldManager, "musicBrainzID"), "the create is import-worker's")
	assert.True(t, ownsField(&ar, mediafilespec.FieldManager, "folder"), "the pinned folder is import-worker's")
	assert.Empty(t, e.mediaFiles(), "an artist's file awaits its albums")
	assert.EqualValues(t, 1, e.tally().ItemsCreated)

	require.NoError(t, e.observe(music, "Nirvana/02 - In Bloom.flac", artistAt(music.Name), nil))
	var all catalogv1alpha1.ArtistList
	require.NoError(t, e.c.List(e.ctx, &all, client.InNamespace(e.ns)))
	assert.Len(t, all.Items, 1, "the second observation of the folder found the artist")
	p := e.tally()
	assert.EqualValues(t, 1, p.ItemsCreated, "the artist was created once")
	assert.Empty(t, p.Unmatched)
	assert.Empty(t, e.mediaFiles())
}

// ownsField reports whether manager holds spec.<field> in obj's
// managedFields (read straight from the apiserver, which keeps them).
func ownsField(obj client.Object, manager k8s.FieldManager, field string) bool {
	for _, mf := range obj.GetManagedFields() {
		if mf.Manager != manager.String() || mf.FieldsV1 == nil {
			continue
		}
		var fields map[string]any
		if json.Unmarshal(mf.FieldsV1.GetRawBytes(), &fields) != nil {
			continue
		}
		if spec, ok := fields["f:spec"].(map[string]any); ok {
			if _, ok := spec["f:"+field]; ok {
				return true
			}
		}
	}
	return false
}

// An ImportExclusion of the artist's MusicBrainz id -- the one the delete
// dialog writes for a deleted Artist (librarydelete's kindSpec, kind artist
// under the musicbrainz key), indexed into the exclusions bucket by the
// importexclusion controller -- refuses the create: the folder is listed
// unmatched, excluded, naming the exclusion, and no Artist is made.
func TestAnExcludedArtistIsNotCreated(t *testing.T) {
	e := newScanEnv(t, "scanapply-artist-excluded")
	music := e.root("music", catalogv1alpha1.RootFolderKindMusic, catalogv1alpha1.RootDefaults{QualityProfileRef: "lossless"})
	ex := &catalogv1alpha1.ImportExclusion{
		ObjectMeta: metav1.ObjectMeta{Namespace: e.ns, Name: "no-nirvana"},
		Spec: catalogv1alpha1.ImportExclusionSpec{
			Kind: catalogv1alpha1.ExclusionKindArtist, Title: "Nirvana", Reason: "deleted from the library",
			ExternalIDs: map[string]string{catalogv1alpha1.ExclusionIDKeyMusicBrainz: nirvanaMBID},
		},
	}
	require.NoError(t, e.c.Create(e.ctx, ex))
	_, err := (&importexclusion.Reconciler{Client: e.c, Bus: e.bus}).Reconcile(e.ctx,
		ctrl.Request{NamespacedName: client.ObjectKeyFromObject(ex)})
	require.NoError(t, err)

	require.NoError(t, e.observe(music, "Nirvana/01 - Smells Like Teen Spirit.flac", artistAt(music.Name), nil))

	var all catalogv1alpha1.ArtistList
	require.NoError(t, e.c.List(e.ctx, &all, client.InNamespace(e.ns)))
	assert.Empty(t, all.Items, "an excluded artist was created")
	p := e.tally()
	require.Len(t, p.Unmatched, 1)
	assert.Contains(t, p.Unmatched[0].Reason, "excluded")
	assert.Contains(t, p.Unmatched[0].Reason, e.ns+"/no-nirvana")
	assert.Zero(t, p.ItemsCreated)
}

// A root without a default qualityProfileRef cannot create the artist,
// which requires one: the folder is listed unmatched saying so.
func TestAnArtistCreateNeedsTheRootsDefaultProfile(t *testing.T) {
	e := newScanEnv(t, "scanapply-artist-noprofile")
	music := e.root("music", catalogv1alpha1.RootFolderKindMusic, catalogv1alpha1.RootDefaults{})

	require.NoError(t, e.observe(music, "Nirvana/01 - Smells Like Teen Spirit.flac", artistAt(music.Name), nil))

	var all catalogv1alpha1.ArtistList
	require.NoError(t, e.c.List(e.ctx, &all, client.InNamespace(e.ns)))
	assert.Empty(t, all.Items)
	p := e.tally()
	require.Len(t, p.Unmatched, 1)
	assert.Contains(t, p.Unmatched[0].Reason, "sets no default qualityProfileRef, which a new artist requires")
	assert.Equal(t, "Nirvana/01 - Smells Like Teen Spirit.flac", p.Unmatched[0].Path)
}

// A root scan creates the Author its folder names (D16) as it does an
// Artist, with works in English alone (metadataProfile.allowedLanguages
// [en], Add New's): every book monitored, no search on add, the folder
// pinned. The root's monitorNewItems "new", which only an artist has,
// leaves an Author the CRD's default (all), where passing it through would
// be refused by the Author's enum and dead-letter the observation.
func TestAScanCreatesTheAuthorItsFolderNames(t *testing.T) {
	e := newScanEnv(t, "scanapply-author")
	books := e.root("books", catalogv1alpha1.RootFolderKindBook, catalogv1alpha1.RootDefaults{
		QualityProfileRef: "ebook", MonitorNewItems: catalogv1alpha1.MonitorNewItemsNew,
	})
	at := &schema.ScanAttribution{
		Kind: string(commonv1.MediaKindAuthor), ProviderID: "openlibrary:OL26320A", Evidence: "folder",
		Create: &schema.ItemCreate{
			Kind: string(commonv1.MediaKindAuthor), OpenLibraryID: "OL26320A", Title: "J. R. R. Tolkien",
			RootFolderRef: books.Name, Folder: "J.R.R. Tolkien",
		},
	}

	require.NoError(t, e.observe(books, "J.R.R. Tolkien/The Hobbit.epub", at, nil))

	var au catalogv1alpha1.Author
	require.NoError(t, e.c.Get(e.ctx, types.NamespacedName{Namespace: e.ns, Name: names.Author("J. R. R. Tolkien", "OL26320A")}, &au))
	assert.Equal(t, "OL26320A", au.Spec.OpenLibraryID)
	assert.Equal(t, ptr.To("J.R.R. Tolkien"), au.Spec.Folder)
	assert.Equal(t, "ebook", au.Spec.QualityProfileRef)
	assert.Equal(t, ptr.To(true), au.Spec.Monitored, "the root's defaults.monitored, true by default")
	assert.Equal(t, catalogv1alpha1.MonitorNewChildrenAll, au.Spec.MonitorNewItems)
	assert.Equal(t, []string{"en"}, au.Spec.MetadataProfile.AllowedLanguages)
	assert.Equal(t, catalogv1alpha1.AuthorMonitorAll, au.Spec.AddOptions.Monitor)
	assert.Equal(t, ptr.To(false), au.Spec.AddOptions.SearchForMissing)
	assert.Empty(t, e.mediaFiles(), "an author's file awaits its books")
}

// A root scan creates the Audiobook its "<author>/<title>" folder names
// (D16) and, unlike a container, records each part the observation names
// against it, with its part index: the audiobook is its files' own item.
// The Audiobook is pinned to the two-segment folder, takes the region the
// agent resolved its ASIN in, and links the one Author of the namespace
// named as the author folder is (release.TitleNorm, Add New's rule); a
// namespace with two Authors of the name links none, and creates no Author.
func TestAScanCreatesTheAudiobookAndRecordsItsParts(t *testing.T) {
	e := newScanEnv(t, "scanapply-audiobook")
	audio := e.root("audio", catalogv1alpha1.RootFolderKindAudiobook, catalogv1alpha1.RootDefaults{
		QualityProfileRef: "spoken", Region: catalogv1alpha1.AudiobookRegionUS,
	})
	e.author("tolkien", "OL26320A", "J. R. R. Tolkien")
	e.author("le-guin", "OL31467A", "Ursula K. Le Guin")
	e.author("le-guin-dup", "OL31468A", "Ursula K. Le Guin")
	hobbit := func(part int32) (*schema.ScanAttribution, *schema.FrozenFields) {
		return &schema.ScanAttribution{
				Kind: string(commonv1.MediaKindAudiobook), ProviderID: "asin:B0099SKT2Y", Evidence: "folder",
				Create: &schema.ItemCreate{
					Kind: string(commonv1.MediaKindAudiobook), ASIN: "B0099SKT2Y", Region: "uk", Title: "The Hobbit",
					RootFolderRef: audio.Name, Folder: "J.R.R. Tolkien/The Hobbit",
				},
			},
			&schema.FrozenFields{PartIndex: part}
	}

	for part := int32(1); part <= 2; part++ {
		at, frozen := hobbit(part)
		require.NoError(t, e.observe(audio, fmt.Sprintf("J.R.R. Tolkien/The Hobbit/The Hobbit - Part %d.m4b", part), at, frozen))
	}

	name := names.Audiobook("The Hobbit", "B0099SKT2Y")
	var ab catalogv1alpha1.Audiobook
	require.NoError(t, e.c.Get(e.ctx, types.NamespacedName{Namespace: e.ns, Name: name}, &ab))
	assert.Equal(t, ptr.To("J.R.R. Tolkien/The Hobbit"), ab.Spec.Folder)
	assert.Equal(t, catalogv1alpha1.AudiobookRegionUK, ab.Spec.Region, "the region the ASIN was resolved in")
	assert.Equal(t, ptr.To("tolkien"), ab.Spec.AuthorRef, "the one Author of the author folder's name")
	assert.Equal(t, "spoken", ab.Spec.QualityProfileRef)
	assert.Equal(t, ptr.To(true), ab.Spec.Monitored)
	assert.Equal(t, ptr.To(false), ab.Spec.AddOptions.SearchForMissing)
	files := e.mediaFiles()
	require.Len(t, files, 2, "each part is recorded against the audiobook")
	parts := map[int32]string{}
	for _, mf := range files {
		assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: name}, mf.Spec.MediaRef)
		parts[mf.Spec.PartIndex] = mf.Spec.Path
	}
	assert.Equal(t, map[int32]string{
		1: audio.Spec.Path + "/J.R.R. Tolkien/The Hobbit/The Hobbit - Part 1.m4b",
		2: audio.Spec.Path + "/J.R.R. Tolkien/The Hobbit/The Hobbit - Part 2.m4b",
	}, parts)
	p := e.tally()
	assert.EqualValues(t, 1, p.ItemsCreated)
	assert.EqualValues(t, 2, p.FilesMatched)

	at := &schema.ScanAttribution{
		Kind: string(commonv1.MediaKindAudiobook), ProviderID: "asin:B07NJHW5MF", Evidence: "folder",
		Create: &schema.ItemCreate{
			Kind: string(commonv1.MediaKindAudiobook), ASIN: "B07NJHW5MF", Title: "A Wizard of Earthsea",
			RootFolderRef: audio.Name, Folder: "Ursula K. Le Guin/A Wizard of Earthsea",
		},
	}
	require.NoError(t, e.observe(audio, "Ursula K. Le Guin/A Wizard of Earthsea/01.mp3", at, &schema.FrozenFields{PartIndex: 1}))
	var earthsea catalogv1alpha1.Audiobook
	require.NoError(t, e.c.Get(e.ctx, types.NamespacedName{Namespace: e.ns, Name: names.Audiobook("A Wizard of Earthsea", "B07NJHW5MF")}, &earthsea))
	assert.Nil(t, earthsea.Spec.AuthorRef, "two Authors of the name: none is guessed")
	assert.Equal(t, catalogv1alpha1.AudiobookRegionUS, earthsea.Spec.Region, "no region proposed: the root's")
	var authors catalogv1alpha1.AuthorList
	require.NoError(t, e.c.List(e.ctx, &authors, client.InNamespace(e.ns)))
	assert.Len(t, authors.Items, 3, "an audiobook create makes no Author")
}

// author creates an Author with its metadata name, the way the metadata
// gateway's answer lands on it (status.metadata under catalog-metadata).
func (e *scanEnv) author(name, olid, display string) {
	e.t.Helper()
	require.NoError(e.t, e.c.Create(e.ctx, &catalogv1alpha1.Author{
		ObjectMeta: metav1.ObjectMeta{Namespace: e.ns, Name: name},
		Spec:       catalogv1alpha1.AuthorSpec{OpenLibraryID: olid, QualityProfileRef: "ebook", RootFolderRef: "books"},
	}))
	_, err := k8s.PatchStatus(e.ctx, e.c, k8s.ManagerCatalogMetadata, catalogac.Author(name, e.ns).
		WithStatus(catalogac.AuthorStatus().WithMetadata(catalogac.AuthorMetadata().WithName(display))))
	require.NoError(e.t, err)
}
