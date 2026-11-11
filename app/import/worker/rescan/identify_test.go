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

package rescan_test

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/scanprogress"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/app/import/worker/rescan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/names"
)

// observingBus records every scan observation the walk publishes, so a test
// reads what the walk proposed without the manager's applier deciding it.
type observingBus struct {
	events.Bus
	mu  sync.Mutex
	obs []schema.ScanObservation
}

func (b *observingBus) Publish(ctx context.Context, subject string, e *events.Envelope, opts ...events.PublishOption) (events.Receipt, error) {
	if e.Schema == (schema.ScanObservation{}).Schema() {
		var o schema.ScanObservation
		if err := schema.Decode(e.Schema, e.Data, &o); err != nil {
			return events.Receipt{}, err
		}
		b.mu.Lock()
		b.obs = append(b.obs, o)
		b.mu.Unlock()
	}
	return b.Bus.Publish(ctx, subject, e, opts...)
}

// observed is the observations by their path relative to root.
func (b *observingBus) observed(t *testing.T, root string) map[string]schema.ScanObservation {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]schema.ScanObservation, len(b.obs))
	for _, o := range b.obs {
		rel, err := filepath.Rel(root, o.Path)
		require.NoError(t, err)
		_, dup := out[rel]
		require.False(t, dup, "%s observed twice", rel)
		out[rel] = o
	}
	return out
}

// gatewayFake answers the walk's metadata lookups and searches from tables,
// recording what it was asked.
type gatewayFake struct {
	mu       sync.Mutex
	names    map[string]string               // lookup: id -> canonical name
	merged   map[string]string               // lookup: an id MusicBrainz merged -> the artist's current id
	hits     map[string][]metadata.SearchHit // search: text -> hits
	lookups  []map[string]string
	searches []string
	regions  []string
}

func (g *gatewayFake) lookup(_ context.Context, kind commonv1.MediaKind, ids map[string]string) (string, map[string]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lookups = append(g.lookups, ids)
	if kind != commonv1.MediaKindArtist {
		return "", nil, errors.New("unexpected lookup kind " + string(kind))
	}
	id := ids[metadata.KeyMBArtist]
	if cur, ok := g.merged[id]; ok {
		id = cur
	}
	if name, ok := g.names[id]; ok {
		return name, map[string]string{metadata.KeyMBArtist: id}, nil
	}
	return "", nil, errors.New("metadata: not found")
}

func (g *gatewayFake) search(_ context.Context, _ commonv1.MediaKind, text, region string) ([]metadata.SearchHit, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.searches = append(g.searches, text)
	g.regions = append(g.regions, region)
	return g.hits[text], nil
}

// walkFixture is a root folder of kind under a temp directory, a scan of it
// and a worker over a fake client holding objs, its bus recording the
// observations, its gateway the fake's.
type walkFixture struct {
	root string
	ns   string
	scan *catalogv1alpha1.LibraryScan
	w    *rescan.Worker
	bus  *observingBus
	gw   *gatewayFake
}

func newWalkFixture(t *testing.T, kind catalogv1alpha1.RootFolderKind, defaults catalogv1alpha1.RootDefaults, objs ...client.Object) *walkFixture {
	t.Helper()
	const ns = "library"
	dir := t.TempDir()
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "lib"},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: dir, Kind: kind, Defaults: defaults},
	}
	scan := &catalogv1alpha1.LibraryScan{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "tick", UID: "scan-uid"},
		Spec:       catalogv1alpha1.LibraryScanSpec{RootFolderRef: rf.Name, Mode: catalogv1alpha1.ScanModeFull},
	}
	b := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(append(objs, rf, scan)...)
	// The import domain registers both packages' indexes (app/import/agent):
	// the walk reads an album's recorded tracks through fileimport's.
	for _, fi := range append(rescan.FieldIndexes(), fileimport.FieldIndexes()...) {
		b = b.WithIndex(fi.Object, fi.Name, fi.Extract)
	}
	bus := &observingBus{Bus: newBus(t, context.Background())}
	gw := &gatewayFake{names: map[string]string{}, merged: map[string]string{}, hits: map[string][]metadata.SearchHit{}}
	w := &rescan.Worker{
		Client: b.Build(), Bus: bus, Clock: time.Now, MetadataTimeout: time.Second,
		Lookup: gw.lookup, Search: gw.search,
	}
	return &walkFixture{root: dir, ns: ns, scan: scan, w: w, bus: bus, gw: gw}
}

func (f *walkFixture) handle(t *testing.T, dryRun bool) (scanprogress.Progress, *fakeMessage) {
	t.Helper()
	m := newFakeMessage(t, schema.ScanTask{
		LibraryScanRef: schema.Ref{Namespace: f.ns, Name: f.scan.Name, UID: string(f.scan.UID)},
		RootFolderRef:  schema.Ref{Namespace: f.ns, Name: "lib"},
		Path:           f.root, Mode: string(catalogv1alpha1.ScanModeFull), DryRun: dryRun,
	})
	require.NoError(t, f.w.Handle(context.Background(), m))
	got := readProgress(t, context.Background(), f.bus, string(f.scan.UID))
	require.Empty(t, got.Error)
	return got, m
}

func (f *walkFixture) plant(t *testing.T, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		mustWriteFile(t, filepath.Join(f.root, rel), smallFile)
	}
}

// folderEntries is a checkpoint's unmatched folder entries, by path.
func folderEntries(p scanprogress.Progress) map[string]scanprogress.UnmatchedFile {
	out := map[string]scanprogress.UnmatchedFile{}
	for _, u := range p.Unmatched {
		if u.Folder {
			out[u.Path] = u
		}
	}
	return out
}

func hit(title, disambiguation, id string, score int32) metadata.SearchHit {
	return metadata.SearchHit{
		IDs: metadata.ExternalIDs{metadata.KeyMBArtist: id}, Title: title, Disambiguation: disambiguation, Score: score,
	}
}

const (
	nirvanaID    = "5b11f4ce-a62d-471e-81fc-a69a8278c7da"
	portisheadID = "8f6bd1e4-fbe1-4f50-aa9b-94c450ec0f11"
	bushID       = "1b8d0bb8-4d4e-4e8f-9a1e-26b8b3e8c3f2"
	radioheadID  = "a74b1b7f-71a5-4011-9441-d0b5e4122711"
)

var nirvanaFolder = "Nirvana [mbid-" + nirvanaID + "]"

// musicLibrary is a music root of flat and nested artist folders, an
// existing Radiohead with one album whose tracks are listed and one whose
// tracks are not, and the gateway's answers for the folders it is asked
// about. probed collects every file the walk probes, relative to the root.
func musicLibrary(t *testing.T) (f *walkFixture, probed *[]string) {
	t.Helper()
	f = newWalkFixture(t, catalogv1alpha1.RootFolderKindMusic, catalogv1alpha1.RootDefaults{QualityProfileRef: "lossless"},
		&catalogv1alpha1.Artist{
			ObjectMeta: metav1.ObjectMeta{Namespace: "library", Name: "radiohead"},
			Spec:       catalogv1alpha1.ArtistSpec{MusicBrainzID: radioheadID, QualityProfileRef: "lossless", RootFolderRef: "lib"},
			Status:     catalogv1alpha1.ArtistStatus{Metadata: &catalogv1alpha1.ArtistMetadata{Name: "Radiohead"}},
		},
		&catalogv1alpha1.Album{
			ObjectMeta: metav1.ObjectMeta{Namespace: "library", Name: "ok-computer"},
			Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "b1392450-e666-3926-a536-22c65f834433"},
			Status: catalogv1alpha1.AlbumStatus{
				Metadata: &catalogv1alpha1.AlbumMetadata{Title: "OK Computer", ReleaseDate: date(1997)},
				Tracks: []catalogv1alpha1.Track{
					{RecordingID: "airbag", Medium: 1, Number: 1, Title: "Airbag"},
					{RecordingID: "paranoid-android", Medium: 1, Number: 2, Title: "Paranoid Android"},
				},
			},
		},
		&catalogv1alpha1.Album{
			ObjectMeta: metav1.ObjectMeta{Namespace: "library", Name: "kid-a"},
			Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "kid-a-group"},
			Status:     catalogv1alpha1.AlbumStatus{Metadata: &catalogv1alpha1.AlbumMetadata{Title: "Kid A"}},
		},
	)
	f.plant(t,
		filepath.Join(nirvanaFolder, "Nevermind", "01 - Smells Like Teen Spirit.flac"),
		filepath.Join(nirvanaFolder, "Nevermind", "02 - In Bloom.flac"),
		filepath.Join("portishead", "Mysterons.flac"),
		filepath.Join("portishead", "Sour Times.flac"),
		filepath.Join("Bush", "01 - Everything Zen.mp3"),
		filepath.Join("Genesis", "01.flac"),
		filepath.Join("Zzyzx Road", "01.flac"),
		filepath.Join("Kate", "01.flac"),
		filepath.Join("Radiohead", "OK Computer (1997)", "01 - Airbag.flac"),
		filepath.Join("Radiohead", "Kid A", "01 - Everything In Its Right Place.flac"),
		filepath.Join("Radiohead", "Paranoid Android.flac"), // flat: its tags name its album
	)
	probed = &[]string{}
	f.w.ProbeAudio = func(_ context.Context, path string) (mediainfo.AudioProbe, error) {
		rel, _ := filepath.Rel(f.root, path)
		*probed = append(*probed, rel)
		ap := mediainfo.AudioProbe{Codec: "flac", SampleBits: 16}
		switch {
		case filepath.Dir(rel) == "portishead":
			ap.Tags = &commonv1.MediaTags{AlbumArtist: "Portishead", Album: "Dummy", MusicBrainzArtistID: portisheadID}
		case filepath.Base(rel) == "Paranoid Android.flac":
			ap.Tags = &commonv1.MediaTags{AlbumArtist: "Radiohead", Album: "OK Computer", Date: "1997", TrackNumber: 2}
		}
		return ap, nil
	}
	f.gw.names[portisheadID] = "Portishead"
	f.gw.hits["Bush"] = []metadata.SearchHit{
		hit("Bush", "UK rock band", bushID, 100), hit("Kate Bush", "", "kate", 85), hit("Bush", "US rapper", "bush-us", 60),
	}
	f.gw.hits["Genesis"] = []metadata.SearchHit{
		hit("Genesis", "English rock band", "genesis-uk", 100), hit("Genesis", "US psych band", "genesis-us", 100),
	}
	f.gw.hits["Kate"] = []metadata.SearchHit{hit("Kate Bush", "", "kate", 100)} // a lone near name
	return f, probed
}

// A music root's flat artist folders are identified and proposed (2026-11-10
// design D16): a folder naming its MusicBrainz id, one whose files' tags
// agree on an id the gateway's lookup validates, and one the gateway's
// search names exactly once at its top score each become one present
// observation creating the Artist, pinned to the folder, with nothing
// frozen, their files counted as awaiting the artist's albums. A folder the
// search names twice at its top score, one it names nothing for, and one it
// answers only a near name for are each listed once, as a folder, with the
// hits as candidates -- never guessed. An existing artist's file is
// attributed by its album folder and its track (D17), and a file of an
// album whose tracks have not arrived awaits them.
func TestARootScanIdentifiesUnmappedArtistFolders(t *testing.T) {
	f, probed := musicLibrary(t)
	got, m := f.handle(t, false)

	assert.EqualValues(t, 11, got.FilesSeen)
	assert.Equal(t, map[string]int64{"artist": 9}, got.AwaitingChildren,
		"the six folders' files and the album with no tracks await; the attributed track does not")
	assert.Zero(t, got.ItemsCreated, "the walk proposes; only the applier creates")

	obs := f.bus.observed(t, f.root)
	require.Len(t, obs, 5, "one observation per proposed folder, and the two attributed tracks: %v", slices.Sorted(maps.Keys(obs)))
	assertCreated := func(rel, id, title, folder string) {
		t.Helper()
		o, ok := obs[rel]
		require.True(t, ok, "%s was not observed", rel)
		assert.Equal(t, schema.ScanObservationPresent, o.Kind)
		assert.Nil(t, o.Frozen, "a container's proposal freezes no file")
		require.NotNil(t, o.Attribution)
		want := names.Artist(title, id)
		assert.Equal(t, schema.ScanAttribution{
			Kind: "artist", Name: want, ProviderID: "musicbrainz:" + id, Evidence: "folder",
			Create: &schema.ItemCreate{
				Kind: "artist", Name: want, MusicBrainzID: id, QualityProfileRef: "lossless", RootFolderRef: "lib",
				Title: title, Folder: folder,
			},
		}, *o.Attribution)
	}
	assertCreated(filepath.Join(nirvanaFolder, "Nevermind", "01 - Smells Like Teen Spirit.flac"), nirvanaID, "Nirvana", nirvanaFolder)
	assertCreated(filepath.Join("portishead", "Mysterons.flac"), portisheadID, "Portishead", "portishead")
	assertCreated(filepath.Join("Bush", "01 - Everything Zen.mp3"), bushID, "Bush", "Bush")

	track := obs[filepath.Join("Radiohead", "OK Computer (1997)", "01 - Airbag.flac")]
	require.NotNil(t, track.Attribution, "the existing artist's file is attributed")
	assert.Equal(t, "album", track.Attribution.Kind)
	assert.Equal(t, "ok-computer", track.Attribution.Name)
	assert.Equal(t, "airbag", track.Attribution.Track)
	assert.Nil(t, track.Attribution.Create)
	require.NotNil(t, track.Frozen)
	require.NotNil(t, track.Frozen.Quality)
	assert.Equal(t, "FLAC", track.Frozen.Quality.Name)
	flat := obs[filepath.Join("Radiohead", "Paranoid Android.flac")]
	require.NotNil(t, flat.Attribution, "a flat file of an existing artist is attributed by its probed tags (D17)")
	assert.Equal(t, "ok-computer", flat.Attribution.Name)
	assert.Equal(t, "paranoid-android", flat.Attribution.Track)

	folders := folderEntries(got)
	require.Len(t, got.Unmatched, 3, "only the three unidentified folders are listed, each once: %v", got.Unmatched)
	require.Len(t, folders, 3)
	genesis := folders["Genesis"]
	assert.Contains(t, genesis.Reason, `ambiguous: the metadata gateway's search names 2 artists called "Genesis"`)
	assert.Equal(t, []string{"Genesis (English rock band) [genesis-uk]", "Genesis (US psych band) [genesis-us]"}, genesis.Candidates)
	assert.Empty(t, folders["Zzyzx Road"].Candidates)
	assert.Contains(t, folders["Zzyzx Road"].Reason, `no artist of this root folder is named "Zzyzx Road"`)
	kate := folders["Kate"]
	assert.Equal(t, []string{"Kate Bush [kate]"}, kate.Candidates, "a near name is a candidate for a person, never the artist")
	assert.Contains(t, kate.Reason, "a near name is never taken")

	assert.Equal(t, []map[string]string{{metadata.KeyMBArtist: portisheadID}}, f.gw.lookups,
		"the tags' agreed id is validated by the gateway's lookup, once")
	assert.ElementsMatch(t, []string{"Bush", "Genesis", "Zzyzx Road", "Kate"}, f.gw.searches,
		"one search per folder the token, the tags and the catalog did not resolve")
	assert.NotContains(t, *probed, filepath.Join(nirvanaFolder, "Nevermind", "01 - Smells Like Teen Spirit.flac"),
		"a token folder's files are not probed for tags, and a proposed folder's files await")
	assert.Equal(t, 1, countOf(*probed, filepath.Join("portishead", "Sour Times.flac")),
		"both tagged files are read for the folder's artist, and once only")
	assert.GreaterOrEqual(t, m.heartbeats.Load(), int64(5), "every RPC is preceded by a heartbeat")
}

func countOf(list []string, want string) int {
	n := 0
	for _, s := range list {
		if s == want {
			n++
		}
	}
	return n
}

// A dry run asks the same questions and counts what the applier would be
// asked to create, publishing nothing.
func TestADryRunCountsTheProposedArtists(t *testing.T) {
	f, _ := musicLibrary(t)
	got, _ := f.handle(t, true)
	assert.EqualValues(t, 3, got.ItemsCreated)
	assert.EqualValues(t, 2, got.FilesMatched, "the existing artist's two tracks")
	assert.Empty(t, f.bus.observed(t, f.root))
	assert.Len(t, folderEntries(got), 3)
}

// An audiobook root proposes the Audiobook its "<author>/<title>" folder
// names an ASIN of, in the root's region, or the one Audible's search
// answers whose title is the folder's and whose authors name the author
// folder (2026-11-10 design D16): each of its files is one observation
// carrying the create and the part's order. A folder Audible answers
// nothing exact for is listed once, as a folder.
func TestARootScanIdentifiesUnmappedAudiobookFolders(t *testing.T) {
	f := newWalkFixture(t, catalogv1alpha1.RootFolderKindAudiobook, catalogv1alpha1.RootDefaults{
		QualityProfileRef: "spoken", Region: catalogv1alpha1.AudiobookRegionUK,
	})
	tokenFolder := filepath.Join("Terry Goodkind", "Wizard's First Rule [B002V0QK4C]")
	f.plant(t,
		filepath.Join(tokenFolder, "01.mp3"),
		filepath.Join(tokenFolder, "02.mp3"),
		filepath.Join("Frank Herbert", "Dune Messiah", "Dune Messiah - Part 1.mp3"),
		filepath.Join("Someone", "Unknown Book", "01.mp3"),
	)
	f.w.ProbeAudio = func(_ context.Context, path string) (mediainfo.AudioProbe, error) {
		ap := mediainfo.AudioProbe{Codec: "mp3"}
		switch filepath.Base(path) {
		case "01.mp3":
			ap.Tags = &commonv1.MediaTags{TrackNumber: 1}
		case "02.mp3":
			ap.Tags = &commonv1.MediaTags{TrackNumber: 2}
		}
		return ap, nil
	}
	book := func(title, asin string, authors ...string) metadata.SearchHit {
		h := metadata.SearchHit{IDs: metadata.ExternalIDs{metadata.KeyASIN: asin}, Title: title}
		for _, a := range authors {
			h.Authors = append(h.Authors, metadata.NamedRef{Name: a})
		}
		return h
	}
	f.gw.hits["Dune Messiah"] = []metadata.SearchHit{
		book("Dune Messiah (Dramatised)", "B0DRAMA000", "Frank Herbert"),
		book("Dune Messiah", "B002V1OF70", "Frank Herbert"),
		book("Dune Messiah", "B0BRIAN000", "Brian Herbert"), // the title, another author
	}

	got, _ := f.handle(t, false)
	assert.EqualValues(t, 4, got.FilesSeen)
	assert.Equal(t, map[string]int64{"audiobook": 1}, got.AwaitingChildren)
	assert.Equal(t, []string{"uk", "uk"}, f.gw.regions, "Audible is searched in the root's region")
	assert.ElementsMatch(t, []string{"Dune Messiah", "Unknown Book"}, f.gw.searches)

	obs := f.bus.observed(t, f.root)
	require.Len(t, obs, 3)
	wizard := names.Audiobook("Wizard's First Rule", "B002V0QK4C")
	for i, part := range []string{"01.mp3", "02.mp3"} {
		o := obs[filepath.Join(tokenFolder, part)]
		require.NotNil(t, o.Attribution, part)
		assert.Equal(t, "audiobook", o.Attribution.Kind)
		assert.Equal(t, wizard, o.Attribution.Name)
		assert.Equal(t, &schema.ItemCreate{
			Kind: "audiobook", Name: wizard, ASIN: "B002V0QK4C", Region: "uk", QualityProfileRef: "spoken",
			RootFolderRef: "lib", Title: "Wizard's First Rule", Folder: tokenFolder,
		}, o.Attribution.Create, "every part carries the proposal")
		require.NotNil(t, o.Frozen, part)
		assert.EqualValues(t, 1001+i, o.Frozen.PartIndex, "the part's order from its tags")
	}
	dune := obs[filepath.Join("Frank Herbert", "Dune Messiah", "Dune Messiah - Part 1.mp3")]
	require.NotNil(t, dune.Attribution)
	require.NotNil(t, dune.Attribution.Create)
	assert.Equal(t, "B002V1OF70", dune.Attribution.Create.ASIN, "the one hit whose title and author are the folder's")
	assert.Equal(t, filepath.Join("Frank Herbert", "Dune Messiah"), dune.Attribution.Create.Folder)
	assert.EqualValues(t, 1, dune.Frozen.PartIndex, "the part's order from its name")

	unknown := folderEntries(got)[filepath.Join("Someone", "Unknown Book")]
	assert.True(t, unknown.Folder, "the unidentified audiobook folder is listed once, as a folder")
}

// A book root attributes a standalone book's file to its Book without
// asking the gateway anything, and proposes the Author an author folder
// names an Open Library id of, or the one the search names exactly.
func TestARootScanIdentifiesUnmappedAuthorFolders(t *testing.T) {
	f := newWalkFixture(t, catalogv1alpha1.RootFolderKindBook, catalogv1alpha1.RootDefaults{QualityProfileRef: "books"},
		&catalogv1alpha1.Book{
			ObjectMeta: metav1.ObjectMeta{Namespace: "library", Name: "flatland"},
			Spec:       catalogv1alpha1.BookSpec{WorkID: "OL1W", RootFolderRef: ptr.To("lib")},
			Status:     catalogv1alpha1.BookStatus{Metadata: &catalogv1alpha1.BookMetadata{Title: "Flatland"}},
		})
	f.plant(t,
		filepath.Join("Flatland", "Flatland.epub"),
		filepath.Join("Frank Herbert [olid-OL79034A]", "Dune", "Dune.epub"),
		filepath.Join("Ursula K. Le Guin", "The Dispossessed.epub"),
	)
	f.gw.hits["Ursula K. Le Guin"] = []metadata.SearchHit{
		{IDs: metadata.ExternalIDs{metadata.KeyOpenLibraryAuthor: "OL31353A"}, Title: "Ursula K. Le Guin", Year: 1929},
		{IDs: metadata.ExternalIDs{metadata.KeyOpenLibraryAuthor: "OL999A"}, Title: "Ursula Le Guin Society"},
	}

	got, _ := f.handle(t, false)
	assert.Empty(t, got.Unmatched)
	assert.Equal(t, []string{"Ursula K. Le Guin"}, f.gw.searches, "the standalone book and the token folder ask nothing")

	obs := f.bus.observed(t, f.root)
	require.Len(t, obs, 3)
	assert.Equal(t, "flatland", obs[filepath.Join("Flatland", "Flatland.epub")].Attribution.Name)
	herbert := obs[filepath.Join("Frank Herbert [olid-OL79034A]", "Dune", "Dune.epub")].Attribution.Create
	require.NotNil(t, herbert)
	assert.Equal(t, schema.ItemCreate{
		Kind: "author", Name: names.Author("Frank Herbert", "OL79034A"), OpenLibraryID: "OL79034A",
		QualityProfileRef: "books", RootFolderRef: "lib", Title: "Frank Herbert", Folder: "Frank Herbert [olid-OL79034A]",
	}, *herbert)
	leguin := obs[filepath.Join("Ursula K. Le Guin", "The Dispossessed.epub")].Attribution
	require.NotNil(t, leguin.Create)
	assert.Equal(t, "OL31353A", leguin.Create.OpenLibraryID)
	assert.Equal(t, "openlibrary:OL31353A", leguin.ProviderID)
	assert.Equal(t, map[string]int64{"author": 2}, got.AwaitingChildren)
}
