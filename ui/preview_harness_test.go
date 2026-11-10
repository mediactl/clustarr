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

//go:build preview

package ui_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// TestPreviewHarness is not a test but a preview rig (2026-11-10): it
// stands a real apiserver up (envtest, the CRDs from config/crd/bases),
// fills it with one artist and two albums, one author and three books (one
// standalone), one comic and three issues and one audiobook -- the shapes
// the kind pages, the Edit dialog's kind blocks and the search modals need
// -- writes a kubeconfig to $PREVIEW_KUBECONFIG and holds the apiserver up
// until $PREVIEW_STOP exists, so bin/ui can be run against it and checked in
// a browser (CLAUDE.md: a portaled dialog is checked in a browser, and talos
// holds no non-video item yet). Run it from the repo root:
//
//	KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)" \
//	KUBE_FEATURE_ClientsAllowCBOR=false KUBE_FEATURE_ClientsPreferCBOR=false \
//	PREVIEW_KUBECONFIG=/tmp/preview.kubeconfig PREVIEW_STOP=/tmp/preview.stop \
//	go test -tags preview -count=1 -v -timeout 4h -run TestPreviewHarness ./ui/
//
// then, once it prints HARNESS READY, `KUBECONFIG=/tmp/preview.kubeconfig
// bin/ui --auth-mode anonymous --bind-address 127.0.0.1:8099 --nats-url
// nats://127.0.0.1:<a port-forward of clustarr-nats> --namespace default
// --plex-provider=false`; `touch /tmp/preview.stop` ends it. The build tag
// keeps it out of every ordinary test run.
func TestPreviewHarness(t *testing.T) {
	env := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "config", "crd", "bases")}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	defer func() { _ = env.Stop() }()

	kc := clientcmdapi.NewConfig()
	kc.Clusters["envtest"] = &clientcmdapi.Cluster{Server: cfg.Host, CertificateAuthorityData: cfg.CAData}
	kc.AuthInfos["envtest"] = &clientcmdapi.AuthInfo{ClientCertificateData: cfg.CertData, ClientKeyData: cfg.KeyData}
	kc.Contexts["envtest"] = &clientcmdapi.Context{Cluster: "envtest", AuthInfo: "envtest", Namespace: "default"}
	kc.CurrentContext = "envtest"
	kcPath := os.Getenv("PREVIEW_KUBECONFIG")
	require.NoError(t, clientcmd.WriteToFile(*kc, kcPath))

	c, err := client.New(cfg, client.Options{Scheme: libraryTestScheme(t)})
	require.NoError(t, err)
	ctx := context.Background()
	date := func(y int, m time.Month, d int) *metav1.Time { tm := metav1.NewTime(time.Date(y, m, d, 0, 0, 0, 0, time.UTC)); return &tm }
	now := metav1.Now()
	create := func(o client.Object, status func()) {
		require.NoError(t, c.Create(ctx, o), "%T %s", o, o.GetName())
		if status != nil {
			status()
			require.NoError(t, c.Status().Update(ctx, o), "status of %T %s", o, o.GetName())
		}
	}
	meta := func(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Name: name, Namespace: "default"} }

	for kind, path := range map[string]string{"music": "/data/media/music", "books": "/data/media/books", "comics": "/data/media/comics", "audiobooks": "/data/media/audiobooks"} {
		rk := map[string]catalogv1.RootFolderKind{"music": "music", "books": "book", "comics": "comic", "audiobooks": "audiobook"}[kind]
		create(&catalogv1.RootFolder{ObjectMeta: meta(kind), Spec: catalogv1.RootFolderSpec{Path: path, Kind: rk}}, nil)
	}

	artist := &catalogv1.Artist{ObjectMeta: meta("bjork"), Spec: catalogv1.ArtistSpec{MusicBrainzID: "87c5dedd-371d-4a53-9f7f-80522fb7f3cb", QualityProfileRef: "music", RootFolderRef: "music", Monitored: new(true)}}
	create(artist, func() {
		artist.Status = catalogv1.ArtistStatus{Path: "/data/media/music/Björk", AlbumCount: 2, AlbumFileCount: 1, Metadata: &catalogv1.ArtistMetadata{
			Name: "Björk", SortName: "Björk", Type: "Person", Country: "IS", Begin: "1965-11-21", Status: catalogv1.ArtistRunStatusContinuing,
			Overview: "Björk Guðmundsdóttir is an Icelandic singer, songwriter, composer, record producer and actress.", Genres: []string{"Electronic", "Art Pop"},
			ExternalIDs: map[string]string{"musicbrainz": "87c5dedd-371d-4a53-9f7f-80522fb7f3cb", "discogs": "9780"}, SchemaVersion: 8, Language: "en", RefreshedAt: now,
		}}
	})
	for _, a := range []struct {
		name, title, rg string
		year            int
		hasFile         bool
	}{{"debut", "Debut", "d1f1b0e8-0000-4000-8000-000000000001", 1993, true}, {"post", "Post", "d1f1b0e8-0000-4000-8000-000000000002", 1995, false}} {
		al := &catalogv1.Album{ObjectMeta: meta(a.name), Spec: catalogv1.AlbumSpec{ArtistRef: "bjork", ReleaseGroupID: a.rg, Monitored: new(true)}}
		create(al, func() {
			al.Status = catalogv1.AlbumStatus{Metadata: &catalogv1.AlbumMetadata{Title: a.title, AlbumType: "Album", ReleaseDate: date(a.year, time.July, 5), Label: "One Little Indian", SchemaVersion: 8, RefreshedAt: now},
				Tracks: []catalogv1.Track{{RecordingID: a.rg + "-1", Medium: 1, Number: 1, Title: "Human Behaviour", DurationMs: 250000}, {RecordingID: a.rg + "-2", Medium: 1, Number: 2, Title: "Crying", DurationMs: 290000}},
				HasFile: a.hasFile, TrackFileCount: 0, TrackSummary: "0/2 tracks", Phase: commonv1.LeafPhaseWanted, CutoffReason: "CutoffUnmet"}
			if a.hasFile {
				al.Status.TrackFileCount, al.Status.TrackSummary, al.Status.Phase, al.Status.CutoffMet = 2, "2/2 tracks", commonv1.LeafPhaseImported, true
				al.Status.FileQuality = &commonv1.Quality{Name: "FLAC"}
			}
		})
	}

	author := &catalogv1.Author{ObjectMeta: meta("le-guin"), Spec: catalogv1.AuthorSpec{OpenLibraryID: "OL21594A", QualityProfileRef: "ebook", RootFolderRef: "books", Monitored: new(true)}}
	create(author, func() {
		author.Status = catalogv1.AuthorStatus{Path: "/data/media/books/Ursula K. Le Guin", BookCount: 2, Metadata: &catalogv1.AuthorMetadata{
			Name: "Ursula K. Le Guin", SortName: "Le Guin, Ursula K.", Born: "1929-10-21", Died: "2018-01-22", Genres: []string{"Science Fiction", "Fantasy"},
			Overview: "Ursula Kroeber Le Guin was an American author best known for her works of speculative fiction.",
			ExternalIDs: map[string]string{"openlibrary": "OL21594A"}, SchemaVersion: 8, Language: "en", RefreshedAt: now,
		}}
	})
	for _, b := range []struct{ name, title, work string; year int; pages int32; standalone bool }{
		{"the-dispossessed", "The Dispossessed", "OL138052W", 1974, 341, false}, {"the-left-hand-of-darkness", "The Left Hand of Darkness", "OL59799W", 1969, 304, false}, {"standalone", "A Standalone Book", "OL999W", 2001, 200, true}} {
		bk := &catalogv1.Book{ObjectMeta: meta(b.name), Spec: catalogv1.BookSpec{WorkID: b.work, Monitored: new(true)}}
		if b.standalone {
			bk.Spec.QualityProfileRef, bk.Spec.RootFolderRef = new("ebook"), new("books")
		} else {
			bk.Spec.AuthorRef = new("le-guin")
		}
		create(bk, func() {
			bk.Status = catalogv1.BookStatus{Phase: commonv1.LeafPhaseWanted, CutoffReason: "CutoffUnmet", Metadata: &catalogv1.BookMetadata{
				Title: b.title, ReleaseDate: date(b.year, time.May, 1), PageCount: b.pages, Publisher: "Harper & Row", Genres: []string{"Science Fiction"},
				SeriesLinks: []catalogv1.SeriesLink{{Series: "Hainish Cycle", Position: fmt.Sprint(b.pages % 7), Primary: true}},
				Editions: []catalogv1.Edition{{ID: "OL1M", Title: b.title, Language: "en", Format: "Hardcover", Publisher: "Harper & Row", PageCount: b.pages, ReleaseDate: date(b.year, time.May, 1)}},
				SchemaVersion: 8, Language: "en", RefreshedAt: now}}
		})
	}

	comic := &catalogv1.Comic{ObjectMeta: meta("saga"), Spec: catalogv1.ComicSpec{Source: catalogv1.ComicSourceComicVine, SourceID: "4050-56582", Kind: catalogv1.ComicKindComic, QualityProfileRef: "comic", RootFolderRef: "comics", Monitored: new(true)}}
	create(comic, func() {
		comic.Status = catalogv1.ComicStatus{Path: "/data/media/comics/Saga (2012)", IssueFileCount: 1, Metadata: &catalogv1.ComicMetadata{
			Title: "Saga", Publisher: "Image Comics", Year: 2012, VolumeNumber: 1, IssueCount: 72, Manga: catalogv1.MangaFlagNo, AgeRating: "Mature", Status: catalogv1.ComicRunStatusContinuing,
			Genres: []string{"Science Fiction", "Fantasy"}, OriginalLanguage: "en", Overview: "Two soldiers from opposite sides of a galactic war fall in love.",
			ExternalIDs: map[string]string{"comicvine": "4050-56582"}, SchemaVersion: 8, Language: "en", RefreshedAt: now}}
	})
	for i, title := range []string{"Chapter One", "Chapter Two", "Chapter Three"} {
		n := i + 1
		iss := &catalogv1.Issue{ObjectMeta: meta(fmt.Sprintf("saga-%03d.0", n)), Spec: catalogv1.IssueSpec{ComicRef: "saga", Number: fmt.Sprint(n), CalculatedNumberCentis: int32(n * 100), Monitored: new(true)}}
		create(iss, func() {
			iss.Status = catalogv1.IssueStatus{Title: title, Date: date(2012, time.Month(2+n), 14), StoreDate: date(2012, time.Month(2+n), 14), SourceID: fmt.Sprintf("4000-%d", 12344+n),
				Overview: "Marko and Alana flee with their newborn daughter.", Credits: []catalogv1.Credit{{Name: "Brian K. Vaughan", Role: "writer"}, {Name: "Fiona Staples", Role: "artist"}},
				Phase: commonv1.LeafPhaseWanted, CutoffReason: "CutoffUnmet"}
			if n == 1 {
				iss.Status.HasFile, iss.Status.FileFormat, iss.Status.FileQuality, iss.Status.Phase = true, "CBZ", &commonv1.Quality{Name: "CBZ"}, commonv1.LeafPhaseCutoffUnmet
			}
		})
	}

	ab := &catalogv1.Audiobook{ObjectMeta: meta("earthsea-ab"), Spec: catalogv1.AudiobookSpec{ASIN: "B002V0QK4C", QualityProfileRef: "audiobook", RootFolderRef: "audiobooks", Monitored: new(true), AuthorRef: new("le-guin")}}
	create(ab, func() {
		ab.Status = catalogv1.AudiobookStatus{Path: "/data/media/audiobooks/Ursula K. Le Guin/A Wizard of Earthsea", Phase: commonv1.LeafPhaseWanted, CutoffReason: "CutoffUnmet", Metadata: &catalogv1.AudiobookMetadata{
			Title: "A Wizard of Earthsea", Authors: []catalogv1.NamedRef{{Name: "Ursula K. Le Guin", ASIN: "B000AP9A6K"}}, Narrators: []string{"Rob Inglis"}, Publisher: "Recorded Books",
			ReleaseDate: date(2010, time.March, 1), RuntimeMinutes: 430, Language: "en", Overview: "Ged was the greatest sorcerer in Earthsea.",
			Series: &catalogv1.SeriesLink{Series: "Earthsea Cycle", Position: "1", Primary: true}, Chapters: []catalogv1.Chapter{{Title: "Warriors in the Mist", StartMs: 0}, {Title: "The Shadow", StartMs: 1800000}},
			SchemaVersion: 8, FetchLanguage: "en", RefreshedAt: now}}
	})

	fmt.Println("HARNESS READY", kcPath)
	stop := os.Getenv("PREVIEW_STOP")
	for {
		if _, err := os.Stat(stop); err == nil {
			return
		}
		time.Sleep(time.Second)
	}
}
