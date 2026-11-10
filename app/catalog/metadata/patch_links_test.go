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

package metadata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/comicvine"
	"github.com/mediactl/clustarr/pkg/metadata/clients/extid"
	"github.com/mediactl/clustarr/pkg/metadata/clients/hardcover"
	"github.com/mediactl/clustarr/pkg/metadata/clients/metron"
	"github.com/mediactl/clustarr/pkg/metadata/clients/openlibrary"
)

// hardcoverFixtures serves the recorded Hardcover responses by operation
// name, as the client's own tests do, counting the requests in hits.
func hardcoverFixtures(t *testing.T, routes map[string]string) (c *hardcover.Client, hits *int) {
	t.Helper()
	hits = new(int)
	opName := regexp.MustCompile(`^query (\w+)`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		var body struct {
			Query string `json:"query"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		m := opName.FindStringSubmatch(body.Query)
		require.NotNil(t, m)
		name, ok := routes[m[1]]
		require.True(t, ok, "unexpected operation %s", m[1])
		b, err := os.ReadFile("../../../test/data/metadata/hardcover/" + name)
		require.NoError(t, err)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	c, err := hardcover.New(hardcover.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, Token: "t"})
	require.NoError(t, err)
	return c, hits
}

// TestTheHardcoverPageReachesAuthorAndBookStatusAsALink: the author and
// book pages render their Hardcover logo from a status.metadata.links
// entry of type "hardcover" (ui/author.go providerLinksOf, ui/book.go
// bookXLinks), which the Hardcover client records from the row's slug
// (2026-11-10 design §9.3) and the builders carry through linksAC. The
// documents come through the real client over the recorded fixtures, not
// a hand-built model, so the type the ui names is the one the client
// emits.
func TestTheHardcoverPageReachesAuthorAndBookStatusAsALink(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	c, _ := hardcoverFixtures(t, map[string]string{"AuthorByID": "author_51942.json", "BookByID": "book_141397.json"})

	a, err := c.Author(context.Background(), pkgmetadata.ExternalIDs{extid.KeyHardcover: "51942"})
	require.NoError(t, err)
	aac := buildAuthorMetadataAC(a, nil, "", now)
	require.Len(t, aac.Links, 1)
	require.Equal(t, "hardcover", *aac.Links[0].Type, "the type the author page's providerLinksOf reads")
	require.Equal(t, "https://hardcover.app/authors/sharon-m-draper", *aac.Links[0].URL)

	b, err := c.Book(context.Background(), pkgmetadata.ExternalIDs{extid.KeyHardcover: "141397"})
	require.NoError(t, err)
	bac := buildBookMetadataAC(b, nil, "", now)
	require.Len(t, bac.Links, 1)
	require.Equal(t, "hardcover", *bac.Links[0].Type, "the type the book page's bookXLinks reads")
	require.Equal(t, "https://hardcover.app/books/out-of-my-mind", *bac.Links[0].URL)

	// The status shape: the apply renders into the CRD's Link, listType=map
	// by type, so one page per provider and the type within its 32 bytes.
	var link catalogv1alpha1.Link
	raw, err := json.Marshal(bac.Links[0])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &link))
	require.Equal(t, catalogv1alpha1.Link{Type: "hardcover", URL: "https://hardcover.app/books/out-of-my-mind"}, link)
}

// TestTheMetronPageReachesComicStatusAsALink: the comic page renders its
// Metron logo from a status.metadata.links entry of type "metron"
// (ui/comic.go comicLinks; 2026-11-10 design §9.4), which the metron
// client records from the series' slug -- no id addresses a series on its
// site -- and buildComicMetadataAC carries through linksAC into
// ComicMetadata.Links. The document comes through the real client over
// the recorded fixture, so the type the ui names is the one the client
// emits.
func TestTheMetronPageReachesComicStatusAsALink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/series/1/", r.URL.Path)
		b, err := os.ReadFile("../../../test/data/metadata/metron/series_1.json")
		require.NoError(t, err)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	c, err := metron.New(metron.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, Token: "t"})
	require.NoError(t, err)

	v, err := c.Volume(context.Background(), pkgmetadata.ExternalIDs{extid.KeyMetron: "1"})
	require.NoError(t, err)
	require.Equal(t, []pkgmetadata.Link{{Type: "metron", URL: "https://metron.cloud/series/death-of-the-inhumans-2018/"}}, v.Links)

	ac := buildComicMetadataAC(v, nil, "", time.Now())
	require.Len(t, ac.Links, 1)
	require.Equal(t, "metron", *ac.Links[0].Type, "the type the comic page's comicLinks reads")
	require.Equal(t, "https://metron.cloud/series/death-of-the-inhumans-2018/", *ac.Links[0].URL)
	require.Equal(t, "1", ac.ExternalIDs["metron"], "the id rides in externalIDs beside it")

	// The status shape: the CRD's Link, listType=map by type.
	var link catalogv1alpha1.Link
	raw, err := json.Marshal(ac.Links[0])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &link))
	require.Equal(t, catalogv1alpha1.Link{Type: "metron", URL: "https://metron.cloud/series/death-of-the-inhumans-2018/"}, link)
}

// TestTheHardcoverPageReachesAnOpenLibraryAuthorsStatusAsALink: the
// gateway's Lookup is first-success and Open Library, the primary, answers
// an Author CR ahead of Hardcover (registry.go's tie-break), so the
// document carries Open Library's links and never the Hardcover page the
// Hardcover client's own Author records -- an Author with a spec.hardcoverID
// got Hardcover's rating and no logo. The gateway's enrich, after the
// primary lookup, asks every Registry.Pages provider with the ids the
// document and the spec carry (target.go threads spec.hardcoverID beside
// the Open Library key); the lean hardcover.Client.Page answers the page,
// and appended to the document's Links it renders through linksAC as the
// type ui/author.go's providerLinksOf reads, after Open Library's own
// links. Both documents come through the real clients over the recorded
// fixtures, and the page through enrich itself, so the ids it is asked
// with are the ones the gateway sends.
func TestTheHardcoverPageReachesAnOpenLibraryAuthorsStatusAsALink(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	ol := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/authors/OL21594A.json", r.URL.Path)
		b, err := os.ReadFile("../../../test/data/metadata/openlibrary/author_OL21594A.json")
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
	t.Cleanup(ol.Close)
	primary := openlibrary.New("clustarr-test", ol.Client(), ol.URL, pkgmetadata.NewLimiter(rate.Inf, 1))
	hc, hcHits := hardcoverFixtures(t, map[string]string{"AuthorPageByID": "author_page_51942.json"})

	// The spec's ids as target.go builds them: the Open Library key the CR
	// is keyed by, and the hardcover id beside it.
	specIDs := pkgmetadata.ExternalIDs{pkgmetadata.KeyOpenLibraryAuthor: "OL21594A", extid.KeyHardcover: "51942"}
	a, err := primary.Author(context.Background(), specIDs)
	require.NoError(t, err)
	require.Equal(t, "Jane Austen", a.Name)
	for _, l := range a.Links {
		require.NotEqual(t, "hardcover", l.Type, "Open Library records no Hardcover page: the primary's document is the one without the link")
	}

	// The gateway's own path: enrich merges the spec's ids into the
	// document's, asks the registered PageProvider with them and appends
	// the page it answers (registry.go registers the hardcover client in
	// reg.Pages; the worker calls enrich right after the Lookup).
	reg := &pkgmetadata.Registry{Books: []pkgmetadata.BookProvider{primary}, Pages: []pkgmetadata.PageProvider{hc}}
	enrich(context.Background(), reg, commonv1.MediaKindAuthor, specIDs, nil, a)
	require.Contains(t, a.Links, pkgmetadata.Link{Type: "hardcover", URL: "https://hardcover.app/authors/sharon-m-draper"})
	require.Equal(t, "51942", a.IDs[extid.KeyHardcover], "the spec's id merged into the document's")
	require.Equal(t, 1, *hcHits, "one request for the page")
	require.Equal(t, hc.Name(), "hardcover", "the page's type is the provider's Name(), the PageProvider contract that lets the gateway skip a page it holds")

	// Asked again (a refresh), the page is already carried, so it is not
	// doubled -- and not asked for: the request would be spent on an
	// answer thrown away.
	before := len(a.Links)
	enrich(context.Background(), reg, commonv1.MediaKindAuthor, specIDs, nil, a)
	require.Len(t, a.Links, before)
	require.Equal(t, 1, *hcHits, "a page the document carries is not asked for again")

	ac := buildAuthorMetadataAC(a, nil, "", now)
	var got []catalogv1alpha1.Link
	for _, l := range ac.Links {
		got = append(got, catalogv1alpha1.Link{Type: *l.Type, URL: *l.URL})
	}
	require.Contains(t, got, catalogv1alpha1.Link{Type: "hardcover", URL: "https://hardcover.app/authors/sharon-m-draper"}, "the type the author page's providerLinksOf reads")
	require.Equal(t, "hardcover", *ac.Links[len(ac.Links)-1].Type, "appended after the primary's own links")
	require.Equal(t, "51942", ac.ExternalIDs["hardcover"], "the id that addressed the page rides in externalIDs too")
}

// TestTheMetronPageReachesAComicVineComicsStatusAsALink: on a cluster that
// configures ComicVine, the metron client's Volume -- the one place the
// client records a series' Metron page, from the detail's resource_url --
// is never asked for a Comic: the Registry's Lookup is first-success with
// primaries ahead of supplementaries (registry.go's sortProviders), and
// Metron's Resolve, which the crosswalk does ask, reads the series row by
// cv_id and answers ids alone, so ComicMetadata.Links never carried the
// page the comic page's Metron logo reads (ui/comic.go comicLinks,
// 2026-11-10 design §9.4). The gateway's enrich asks metronPages, the
// client as a PageProvider, with the ids the crosswalk settled on -- the
// metron id Resolve just added -- and the page lands as the one further
// request, GET /series/<id>/, appended to the ComicVine document's links.
// ComicVine's document comes through the real client over volume_18257.json's
// recorded shape carrying Death of the Inhumans' values (ComicVine volume
// 4050-111265, which the recorded metron list crosswalks to series 1);
// Metron's answers are the recorded fixtures.
func TestTheMetronPageReachesAComicVineComicsStatusAsALink(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	cv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/volume/4050-111265/", r.URL.Path)
		_, _ = w.Write([]byte(`{"error":"OK","limit":1,"offset":0,"number_of_page_results":1,"number_of_total_results":1,"status_code":1,` +
			`"results":{"id":111265,"name":"Death of the Inhumans","start_year":"2018","publisher":{"id":31,"name":"Marvel"},"count_of_issues":5,` +
			`"description":"<p>The Inhumans are dying.</p>","site_detail_url":"https://comicvine.gamespot.com/death-of-the-inhumans/4050-111265/"}}`))
	}))
	t.Cleanup(cv.Close)
	primary := comicvine.New("test-key", cv.Client(), cv.URL, pkgmetadata.NewLimiter(rate.Inf, 3))

	var seen []string
	m := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		seen = append(seen, key)
		name, ok := map[string]string{
			"/series/?cv_id=111265": "series_list_cv_111265.json",
			"/series/?cv_id=1":      "series_list_empty.json",
			"/series/1/":            "series_1.json",
		}[key]
		require.True(t, ok, "unexpected metron request %s", key)
		b, err := os.ReadFile("../../../test/data/metadata/metron/" + name)
		require.NoError(t, err)
		_, _ = w.Write(b)
	}))
	t.Cleanup(m.Close)
	mc, err := metron.New(metron.Config{HTTPClient: m.Client(), BaseURL: m.URL, Token: "t"})
	require.NoError(t, err)

	// The spec's ids as target.go builds them for a ComicVine-sourced
	// Comic: the source id under the comicvine key.
	specIDs := pkgmetadata.ExternalIDs{pkgmetadata.KeyComicVine: "4050-111265"}
	reg := &pkgmetadata.Registry{
		Comics:    []pkgmetadata.ComicProvider{primary, mc},
		Resolvers: []pkgmetadata.IDResolver{mc},
		Pages:     []pkgmetadata.PageProvider{metronPages{mc}},
	}

	// The primary's document, as the worker's Lookup fetches it: ComicVine
	// answers first and records no page, and Metron is not asked.
	doc, err := reg.Lookup(context.Background(), commonv1.MediaKindComic, specIDs)
	require.NoError(t, err)
	v, ok := doc.(*pkgmetadata.ComicVolume)
	require.True(t, ok)
	require.Equal(t, "Death of the Inhumans", v.Title)
	require.Empty(t, v.Links, "ComicVine records no page: the primary's document is the one without the link")
	require.Empty(t, seen, "the Lookup is first-success: Metron's Volume is never asked beside ComicVine")

	// The gateway's own path, right after the Lookup: the crosswalk adds
	// the metron id by the cv_id row, and the page is asked with it.
	enrich(context.Background(), reg, commonv1.MediaKindComic, specIDs, nil, v)
	metronPage := pkgmetadata.Link{Type: "metron", URL: "https://metron.cloud/series/death-of-the-inhumans-2018/"}
	require.Equal(t, []pkgmetadata.Link{metronPage}, v.Links)
	require.Equal(t, "1", v.IDs[extid.KeyMetron], "the crosswalk's id rides in the document's ids")
	require.Equal(t, "4050-111265", v.IDs[pkgmetadata.KeyComicVine], "the source's id stays")
	require.Equal(t, []string{"/series/?cv_id=111265", "/series/1/"}, seen, "the crosswalk's row by cv_id, then the one detail the page needs, by the id it added")
	require.Equal(t, mc.Name(), metronPage.Type, "the page's type is the provider's Name(), the PageProvider contract")

	// A refresh: the page is carried, so it is neither doubled nor asked
	// for; the one metron request is Resolve's own read by the metron id.
	seen = nil
	enrich(context.Background(), reg, commonv1.MediaKindComic, specIDs, nil, v)
	require.Equal(t, []pkgmetadata.Link{metronPage}, v.Links, "not doubled")
	require.Equal(t, []string{"/series/1/"}, seen, "Resolve's read alone: a page the document carries is not asked for")

	// A Metron-sourced comic (no ComicVine provider) carries the page from
	// Volume itself, and a comic no Metron series answers is left as it is.
	seen = nil
	own, err := mc.Volume(context.Background(), pkgmetadata.ExternalIDs{extid.KeyMetron: "1"})
	require.NoError(t, err)
	require.Equal(t, []pkgmetadata.Link{metronPage}, own.Links)
	link, err := metronPages{mc}.Page(context.Background(), commonv1.MediaKindComic, pkgmetadata.ExternalIDs{pkgmetadata.KeyComicVine: "4050-1"})
	require.NoError(t, err, "a comic Metron does not know is nothing to link, not a failure")
	require.Equal(t, pkgmetadata.Link{}, link)
	link, err = metronPages{mc}.Page(context.Background(), commonv1.MediaKindComic, pkgmetadata.ExternalIDs{"mangadex": "abc"})
	require.NoError(t, err)
	require.Equal(t, pkgmetadata.Link{}, link, "a comic carrying neither id is asked for nothing")
	_, err = metronPages{mc}.Page(context.Background(), commonv1.MediaKindAuthor, pkgmetadata.ExternalIDs{extid.KeyMetron: "1"})
	require.ErrorIs(t, err, pkgmetadata.ErrUnsupported, "no pages for another kind")
	require.Equal(t, []string{"/series/1/", "/series/?cv_id=1"}, seen, "the empty answers cost one lookup and none")

	// The status shape: buildComicMetadataAC carries the page through
	// linksAC as the type ui/comic.go's comicLinks reads, with both ids in
	// externalIDs.
	ac := buildComicMetadataAC(v, nil, "", now)
	require.Len(t, ac.Links, 1)
	require.Equal(t, "metron", *ac.Links[0].Type, "the type the comic page's comicLinks reads")
	require.Equal(t, metronPage.URL, *ac.Links[0].URL)
	require.Equal(t, "4050-111265", ac.ExternalIDs["comicvine"])
	require.Equal(t, "1", ac.ExternalIDs["metron"])
}

// TestBuildRegistryWiresThePageProviders: the two pages above reach status
// only because BuildRegistry puts their clients in reg.Pages -- the
// hardcover client itself, and the metron client as metronPages, the same
// instance that answers comics and the crosswalk (addSupplementary). The
// guard beside TestBuildRegistryWiresEveryProviderType, kept here with the
// tests that read the pages it wires.
func TestBuildRegistryWiresThePageProviders(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "clustarr"},
		Data: map[string][]byte{
			catalogv1alpha1.MetadataSecretKeyAPIKey: []byte("k"),
			catalogv1alpha1.MetadataSecretKeyBearer: []byte("t"),
		},
	}
	var providers []catalogv1alpha1.MetadataProvider
	for _, typ := range []catalogv1alpha1.MetadataProviderType{
		catalogv1alpha1.MetadataProviderOpenLibrary, catalogv1alpha1.MetadataProviderComicVine,
		catalogv1alpha1.MetadataProviderMangaDex, catalogv1alpha1.MetadataProviderHardcover,
		catalogv1alpha1.MetadataProviderMetron,
	} {
		providers = append(providers, catalogv1alpha1.MetadataProvider{
			ObjectMeta: metav1.ObjectMeta{Name: string(typ), Namespace: "clustarr"},
			Spec: catalogv1alpha1.MetadataProviderSpec{
				Type: typ, Enabled: enabled(), Priority: 50, ContactUserAgent: "clustarr-test (test@example.com)",
				SecretRef: &corev1.LocalObjectReference{Name: "creds"},
			},
		})
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(secret).Build()
	reg, err := BuildRegistry(context.Background(), c, providers, http.DefaultClient)
	require.NoError(t, err)

	var pages []string
	for _, p := range reg.Pages {
		pages = append(pages, p.Name())
	}
	require.Equal(t, []string{"hardcover", "metron"}, pages, "the two supplementary providers with pages, and no primary")

	require.Equal(t, reg.Books[1], reg.Pages[0], "the hardcover client answers books and pages alike")
	mp, ok := reg.Pages[1].(metronPages)
	require.True(t, ok, "the metron client is wired through metronPages")
	var comics, resolvers []pkgmetadata.Provider
	for _, p := range reg.Comics {
		comics = append(comics, p)
	}
	for _, p := range reg.Resolvers {
		resolvers = append(resolvers, p)
	}
	require.Contains(t, comics, pkgmetadata.Provider(mp.Client), "the same metron client answers comics")
	require.Contains(t, resolvers, pkgmetadata.Provider(mp.Client), "and the crosswalk")
}
