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

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/importarr/worker/fileimport"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// The non-video probes are keyed by the ids testdata/metadata's fixtures
// carry, so the fake providers below serve those fixtures verbatim and the
// gateway maps real provider-shaped responses.
const (
	nvArtistMBID     = "a74b1b7f-71a5-4011-9441-d0b5e4122711" // Radiohead
	nvReleaseGroupID = "0b56cf2b-8e64-39e0-b6d5-9a89e46be9f6" // Kid A
	nvAuthorOLID     = "OL21594A"                             // Jane Austen
	nvWorkID         = "OL138052W"                            // Pride and Prejudice
	nvComicVolume    = "4050-18257"                           // Batman
	nvASIN           = "B0036I54I6"                           // The Hobbit
	// nvRegion is deliberately not AudiobookSpec.Region's "us" default, so
	// only a region read from the Audiobook can put it on the wire.
	nvRegion = catalogv1alpha1.AudiobookRegionUK
)

// fakeMetadataProviders stands in for MusicBrainz, Open Library, ComicVine
// and Audnexus: one httptest server each, which catalogarr/all's metadata
// gateway reaches through MetadataProvider objects whose spec.baseURL names
// them. Faking at the HTTP edge rather than at the RPC is what keeps the
// proof honest here: catalogarr/all runs the REAL gateway, which answers
// rpc.catalogarr.metadata.lookup in the catalogarr queue group, so a fake
// RPC responder beside it would race it for every request. Nothing leaves
// the machine.
type fakeMetadataProviders struct {
	mu   sync.Mutex
	seen []string // "<type> <path>?<raw query>"
	urls map[catalogv1alpha1.MetadataProviderType]string
}

func startFakeMetadataProviders(t *testing.T) *fakeMetadataProviders {
	t.Helper()
	f := &fakeMetadataProviders{urls: map[catalogv1alpha1.MetadataProviderType]string{}}
	serve := func(typ catalogv1alpha1.MetadataProviderType, route func(path string, q url.Values) string) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			f.seen = append(f.seen, string(typ)+" "+r.URL.Path+"?"+r.URL.RawQuery)
			f.mu.Unlock()
			fixture := route(strings.TrimSuffix(r.URL.Path, "/"), r.URL.Query())
			if fixture == "" {
				http.NotFound(w, r)
				return
			}
			body, err := os.ReadFile(filepath.Join("../../testdata/metadata", fixture))
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		}))
		t.Cleanup(srv.Close)
		f.urls[typ] = srv.URL
	}

	serve(catalogv1alpha1.MetadataProviderMusicBrainz, func(p string, q url.Values) string {
		switch {
		case p == "/artist/"+nvArtistMBID:
			return "musicbrainz/artist_radiohead.json"
		case p == "/release-group" && q.Get("artist") == nvArtistMBID:
			return "musicbrainz/browse_releasegroups_radiohead.json"
		case p == "/release-group/"+nvReleaseGroupID:
			return "musicbrainz/releasegroup_kid_a.json"
		// Client.Album also browses the release group's releases
		// (musicbrainzws2's BrowseReleases: "/release/" filtered by a
		// release-group query parameter) and fails the whole call if that
		// browse fails. The single-page recorded fixture is The Bends',
		// the same one test/fixtures/nonvideostub serves for Kid A: no
		// Kid A release browse is recorded, and nothing here asserts on
		// tracks.
		case p == "/release" && q.Get("release-group") == nvReleaseGroupID:
			return "musicbrainz/browse_releases_the_bends.json"
		}
		return ""
	})
	serve(catalogv1alpha1.MetadataProviderOpenLibrary, func(p string, _ url.Values) string {
		switch p {
		case "/authors/" + nvAuthorOLID + ".json":
			return "openlibrary/author_OL21594A.json"
		case "/authors/" + nvAuthorOLID + "/works.json":
			return "openlibrary/works_OL21594A.json"
		case "/works/" + nvWorkID + ".json":
			return "openlibrary/work_OL138052W.json"
		// Client.Book also fetches the work's editions and fails the whole
		// call if that fetch fails.
		case "/works/" + nvWorkID + "/editions.json":
			return "openlibrary/editions_OL138052W.json"
		case "/search.json":
			return "openlibrary/search_pride_and_prejudice.json"
		}
		return ""
	})
	// Strict about the volume id, in both of its shapes: GET /volume/{guid}
	// answers only the prefixed guid "4050-18257", and GET /issues only
	// filter=volume:18257, the bare numeric id -- ComicVine's real API, and
	// exactly what pkg/metadata/clients/comicvine's normalizeVolumeID
	// derives from Comic.spec.sourceID for each call. This fake used to
	// accept any /volume/ path and ignore the filter, and that leniency is
	// how passing sourceID unchanged to both calls shipped (fixed by Q-3,
	// bbdfc03): no single id satisfies both endpoints, yet the lenient fake
	// answered both. test/fixtures/nonvideostub enforces the same two
	// shapes for the e2e suite.
	serve(catalogv1alpha1.MetadataProviderComicVine, func(p string, q url.Values) string {
		switch {
		case p == "/volume/"+nvComicVolume:
			return "comicvine/volume_18257.json"
		case p == "/issues" && q.Get("filter") == "volume:18257":
			return "comicvine/issues_volume_18257.json"
		case p == "/search":
			return "comicvine/search_batman.json"
		}
		return ""
	})
	serve(catalogv1alpha1.MetadataProviderAudnexus, func(p string, _ url.Values) string {
		switch {
		case p == "/books/"+nvASIN:
			return "audnexus/book_B0036I54I6.json"
		case strings.HasSuffix(p, "/chapters"):
			return "audnexus/chapters_B0036I54I6.json"
		}
		return ""
	})
	return f
}

// saw reports whether provider typ received a GET for path carrying
// query parameter key=value.
func (f *fakeMetadataProviders) saw(typ catalogv1alpha1.MetadataProviderType, path, key, value string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.seen {
		gotTyp, rest, _ := strings.Cut(s, " ")
		gotPath, rawQuery, _ := strings.Cut(rest, "?")
		if gotTyp != string(typ) || gotPath != path {
			continue
		}
		if q, err := url.ParseQuery(rawQuery); err == nil && q.Get(key) == value {
			return true
		}
	}
	return false
}

// prepareNonVideoCatalog creates the four MetadataProviders the gateway
// needs, pointed at fake. It must run BEFORE catalogarr starts:
// catalogmetadata.Setup builds its provider registry once, from the
// MetadataProviders that exist when the gateway starts.
func prepareNonVideoCatalog(t *testing.T, cfg *rest.Config, fake *fakeMetadataProviders) {
	t.Helper()
	ctx := context.Background()
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "nv-comicvine", Namespace: "default"},
		Data:       map[string][]byte{catalogv1alpha1.MetadataSecretKeyAPIKey: []byte("start-test-key")},
	}
	objs := []client.Object{secret}
	for typ, base := range fake.urls {
		p := &catalogv1alpha1.MetadataProvider{
			ObjectMeta: metav1.ObjectMeta{Name: "nv-" + string(typ), Namespace: "default"},
			Spec: catalogv1alpha1.MetadataProviderSpec{
				Type:             typ,
				Enabled:          ptr.To(true),
				BaseURL:          ptr.To(base),
				ContactUserAgent: "Clustarr-start-test/0 (https://github.com/mediactl/clustarr)",
			},
		}
		if typ == catalogv1alpha1.MetadataProviderComicVine {
			p.Spec.SecretRef = &corev1.LocalObjectReference{Name: secret.Name}
		}
		objs = append(objs, p)
	}
	for _, o := range objs {
		if err := c.Create(ctx, o); err != nil {
			t.Fatalf("create %T %s: %v", o, o.GetName(), err)
		}
	}
	t.Cleanup(func() {
		for _, o := range objs {
			_ = c.Delete(context.Background(), o)
		}
	})
}

// verifyNonVideoCatalog gives each of the seven non-video reconcilers plan
// task G2-5 registered its first piece of work, through the real metadata
// gateway, and waits for it:
//
//   - Artist fans out an Album from the gateway's release-group listing
//     (the lookupAlbums RPC), and its own MetadataTask fills its metadata;
//   - Album publishes its own MetadataTask and writes its phase;
//   - Author fans out a Book from the works listing (lookupBooks);
//   - Book (the fanned-out shape) publishes its task and writes its phase;
//   - Comic fans out an Issue from the issue listing (lookupIssues) and
//     writes the Issue's provider fields under catalogarr-fanout;
//   - Issue writes its own state, under catalogarr;
//   - Audiobook publishes a MetadataTask the gateway turns into an Audnexus
//     request for ITS region, and writes its phase.
//
// Each wait names a value only that reconciler writes, so a controller
// left out of setupControllers fails its own line rather than a later one.
func verifyNonVideoCatalog(t *testing.T, cfg *rest.Config, fake *fakeMetadataProviders) {
	t.Helper()
	ctx := context.Background()
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}

	// Root folders first: a parent whose metadata has landed and whose
	// root folder is missing requeues for a minute before it fans out again.
	var objs []client.Object
	for kind, path := range map[catalogv1alpha1.RootFolderKind]string{
		catalogv1alpha1.RootFolderKindMusic:     "/data/media/music",
		catalogv1alpha1.RootFolderKindBook:      "/data/media/books",
		catalogv1alpha1.RootFolderKindAudiobook: "/data/media/audiobooks",
		catalogv1alpha1.RootFolderKindComic:     "/data/media/comics",
	} {
		objs = append(objs, &catalogv1alpha1.RootFolder{
			ObjectMeta: metav1.ObjectMeta{Name: "nv-" + string(kind), Namespace: "default"},
			Spec:       catalogv1alpha1.RootFolderSpec{Kind: kind, Path: path},
		})
	}
	artist := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "nv-radiohead", Namespace: "default"},
		Spec: catalogv1alpha1.ArtistSpec{
			MusicBrainzID: nvArtistMBID, QualityProfileRef: "music-lossless", RootFolderRef: "nv-music",
			MetadataProfile: catalogv1alpha1.MusicMetadataProfile{
				PrimaryTypes: []string{"album"}, SecondaryTypes: []string{"studio"}, ReleaseStatuses: []string{"official"},
			},
		},
	}
	author := &catalogv1alpha1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: "nv-austen", Namespace: "default"},
		Spec: catalogv1alpha1.AuthorSpec{
			OpenLibraryID: nvAuthorOLID, QualityProfileRef: "ebook", RootFolderRef: "nv-book",
		},
	}
	comic := &catalogv1alpha1.Comic{
		ObjectMeta: metav1.ObjectMeta{Name: "nv-batman", Namespace: "default"},
		Spec: catalogv1alpha1.ComicSpec{
			Source: catalogv1alpha1.ComicSourceComicVine, SourceID: nvComicVolume,
			QualityProfileRef: "comic", RootFolderRef: "nv-comic",
		},
	}
	audiobook := &catalogv1alpha1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: "nv-hobbit", Namespace: "default"},
		Spec: catalogv1alpha1.AudiobookSpec{
			ASIN: nvASIN, Region: nvRegion, QualityProfileRef: "audiobook", RootFolderRef: "nv-audiobook",
		},
	}
	objs = append(objs, artist, author, comic, audiobook)
	for _, o := range objs {
		if err := c.Create(ctx, o); err != nil {
			t.Fatalf("create %T %s: %v", o, o.GetName(), err)
		}
	}
	t.Cleanup(func() {
		for _, o := range objs {
			_ = c.Delete(context.Background(), o)
		}
	})

	// Artist -> Album.
	var album catalogv1alpha1.Album
	waitForLong(t, "the Artist controller to fan out an Album from the gateway's release-group listing", func() bool {
		var list catalogv1alpha1.AlbumList
		if c.List(ctx, &list, client.InNamespace("default")) != nil {
			return false
		}
		for _, a := range list.Items {
			if a.Spec.ArtistRef == artist.Name && a.Spec.ReleaseGroupID == nvReleaseGroupID {
				album = a
				return true
			}
		}
		return false
	})
	if ref := metav1.GetControllerOf(&album); ref == nil || ref.Kind != "Artist" || ref.Name != artist.Name {
		t.Errorf("Album %s is controlled by %+v, want Artist %s", album.Name, ref, artist.Name)
	}
	waitForLong(t, "the Artist's own MetadataTask to fill its metadata", func() bool {
		var got catalogv1alpha1.Artist
		return c.Get(ctx, client.ObjectKeyFromObject(artist), &got) == nil &&
			got.Status.Metadata != nil && got.Status.Metadata.Name == "Radiohead"
	})
	waitForLong(t, "the Album controller to fetch its metadata and write its phase", func() bool {
		var got catalogv1alpha1.Album
		return c.Get(ctx, client.ObjectKeyFromObject(&album), &got) == nil &&
			got.Status.Metadata != nil && got.Status.Metadata.Title == "Kid A" && got.Status.Phase != ""
	})

	// Author -> Book.
	var book catalogv1alpha1.Book
	waitForLong(t, "the Author controller to fan out a Book from the gateway's works listing", func() bool {
		var list catalogv1alpha1.BookList
		if c.List(ctx, &list, client.InNamespace("default")) != nil {
			return false
		}
		for _, b := range list.Items {
			if b.Spec.AuthorRef != nil && *b.Spec.AuthorRef == author.Name && b.Spec.WorkID == nvWorkID {
				book = b
				return true
			}
		}
		return false
	})
	waitForLong(t, "the Book controller to fetch its metadata and write its phase", func() bool {
		var got catalogv1alpha1.Book
		return c.Get(ctx, client.ObjectKeyFromObject(&book), &got) == nil &&
			got.Status.Metadata != nil && got.Status.Phase != ""
	})

	// Comic -> Issue: two managers on one Issue's status.
	var iss catalogv1alpha1.Issue
	waitForLong(t, "the Comic controller to fan out an Issue and write its provider fields", func() bool {
		var list catalogv1alpha1.IssueList
		if c.List(ctx, &list, client.InNamespace("default")) != nil {
			return false
		}
		for _, i := range list.Items {
			if i.Spec.ComicRef == comic.Name && i.Status.Title == "Batman #1" {
				iss = i
				return true
			}
		}
		return false
	})
	waitForLong(t, "the Issue controller to set the fanned-out Issue's state", func() bool {
		return c.Get(ctx, client.ObjectKeyFromObject(&iss), &iss) == nil && iss.Status.State != ""
	})
	if got := statusManagers(iss.ManagedFields); !got[string(k8s.ManagerCatalogarrFanout)] || !got[string(k8s.ManagerCatalogarr)] {
		t.Errorf("Issue %s status is applied by %v, want both %s (Comic's fan-out) and %s (the Issue controller)",
			iss.Name, got, k8s.ManagerCatalogarrFanout, k8s.ManagerCatalogarr)
	}
	// The Issue controller's catalog item event, which catalogarr/run.go
	// could publish only once it handed the reconciler the bus (X14; until
	// then a nil Bus published nothing, silently). This case also runs the
	// history sink, which turns that event into an Event on the Issue.
	waitForLong(t, "the Issue controller's item event to reach the history sink", func() bool {
		var list eventsv1.EventList
		if c.List(ctx, &list, client.InNamespace("default")) != nil {
			return false
		}
		for _, e := range list.Items {
			if e.Regarding.Kind == "Issue" && e.Regarding.Name == iss.Name &&
				e.ReportingController == "catalogarr-history" {
				return true
			}
		}
		return false
	})

	// Audiobook, through the region it names.
	waitForLong(t, "the Audiobook controller's MetadataTask to reach Audnexus for its own region", func() bool {
		return fake.saw(catalogv1alpha1.MetadataProviderAudnexus, "/books/"+nvASIN, "region", string(nvRegion))
	})
	waitForLong(t, "the Audiobook controller to write its phase over the fetched metadata", func() bool {
		var got catalogv1alpha1.Audiobook
		return c.Get(ctx, client.ObjectKeyFromObject(audiobook), &got) == nil &&
			got.Status.Metadata != nil && got.Status.Phase != ""
	})
}

// statusManagers is the set of field managers with an entry on the status
// subresource.
func statusManagers(entries []metav1.ManagedFieldsEntry) map[string]bool {
	out := map[string]bool{}
	for _, e := range entries {
		if e.Subresource == "status" {
			out[e.Manager] = true
		}
	}
	return out
}

// verifyRetrigger gives fileimport's Retrigger (registered by plan task
// G2-5) its first piece of work on the leader importarr/all has become: a
// Blocked Download gains an import-target annotation, the Retrigger re-queues
// its ImportTask, and the file-import worker in the same process re-runs the
// import against the new target -- which does not exist, so it blocks again
// with a message naming it. Nothing else in this case publishes an
// ImportTask (grabarr is not running), so the new message is the proof.
func verifyRetrigger(t *testing.T, cfg *rest.Config) {
	t.Helper()
	ctx := context.Background()
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	magnet := "magnet:?xt=urn:btih:" + strings.Repeat("b", 40)
	dl := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: "retrigger-probe", Namespace: "default"},
		Spec: downloadv1alpha1.DownloadSpec{
			Protocol: commonv1alpha1.ProtocolTorrent,
			Source:   downloadv1alpha1.DownloadSource{MagnetURL: &magnet},
			Release: commonv1alpha1.ReleaseInfo{
				GUID: "retrigger-probe", IndexerRef: "idx", IndexerName: "Example",
				Title: "Retrigger Probe 2020 1080p WEB-DL x264-GRP", Protocol: commonv1alpha1.ProtocolTorrent,
				// Set although optional: the CRD's release-identity CEL rule
				// reads it unguarded on every status apply (see
				// importarr/worker/fileimport's envtest fixture).
				InfoHash: strings.Repeat("b", 40),
			},
			Target: commonv1alpha1.MediaRef{Kind: commonv1alpha1.MediaKindMovie, Name: "retrigger-probe"},
		},
	}
	if err := c.Create(ctx, dl); err != nil {
		t.Fatalf("create Download: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), dl) })

	// The state an earlier import left behind: completed, and Blocked.
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr, downloadac.Download(dl.Name, dl.Namespace).
		WithStatus(downloadac.DownloadStatus().
			WithPhase(downloadv1alpha1.DownloadPhaseCompleted).
			WithContentRoot("/data/downloads/retrigger-probe"))); err != nil {
		t.Fatalf("complete the Download: %v", err)
	}
	const seeded = "seeded by the start test"
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerImportarr, downloadac.Download(dl.Name, dl.Namespace).
		WithStatus(downloadac.DownloadStatus().WithImport(downloadac.ImportState().
			WithState(downloadv1alpha1.ImportPhaseBlocked).WithMessage(seeded)))); err != nil {
		t.Fatalf("block the Download's import: %v", err)
	}

	// The user's instruction.
	var cur downloadv1alpha1.Download
	if err := c.Get(ctx, client.ObjectKeyFromObject(dl), &cur); err != nil {
		t.Fatalf("get Download: %v", err)
	}
	patch := client.MergeFrom(cur.DeepCopy())
	cur.Annotations = map[string]string{fileimport.AnnotationImportTarget: "movie/retrigger-target"}
	if err := c.Patch(ctx, &cur, patch); err != nil {
		t.Fatalf("annotate the Download: %v", err)
	}

	var got downloadv1alpha1.Download
	waitFor(t, "the Retrigger to re-queue the blocked import and the worker to re-run it", func() bool {
		return c.Get(ctx, client.ObjectKeyFromObject(dl), &got) == nil && got.Status.Import != nil &&
			strings.Contains(got.Status.Import.Message, `"retrigger-target"`)
	})
	if got.Status.Import.State != downloadv1alpha1.ImportPhaseBlocked {
		t.Errorf("status.import.state = %q (%q), want Blocked on the missing target", got.Status.Import.State, got.Status.Import.Message)
	}
}
