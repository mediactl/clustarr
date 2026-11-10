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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/extid"
	"github.com/mediactl/clustarr/pkg/metadata/clients/hardcover"
	"github.com/mediactl/clustarr/pkg/metadata/clients/metron"
	"github.com/mediactl/clustarr/pkg/metadata/clients/openlibrary"
)

// hardcoverFixtures serves the recorded Hardcover responses by operation
// name, as the client's own tests do.
func hardcoverFixtures(t *testing.T, routes map[string]string) *hardcover.Client {
	t.Helper()
	opName := regexp.MustCompile(`^query (\w+)`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	return c
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
	c := hardcoverFixtures(t, map[string]string{"AuthorByID": "author_51942.json", "BookByID": "book_141397.json"})

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

// TestTheMetronPageStopsAtTheComicVolume records where the Metron link
// ends today: the metron client puts the series page on ComicVolume.Links,
// and ComicMetadata has no links field for buildComicMetadataAC to carry
// it into (api/ leftover; the comic page builds its logos from the
// externalIDs alone until then). When ComicMetadata gains Links, this
// test is the one to flip.
func TestTheMetronPageStopsAtTheComicVolume(t *testing.T) {
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
	raw, err := json.Marshal(ac)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "metron.cloud/series", "ComicMetadata carries no links; the page is on the model alone")
	require.Equal(t, "1", ac.ExternalIDs["metron"])
}

// TestTheHardcoverPageReachesAnOpenLibraryAuthorsStatusAsALink: the
// gateway's Lookup is first-success and Open Library, the primary, answers
// an Author CR ahead of Hardcover (registry.go's tie-break), so the
// document carries Open Library's links and never the Hardcover page the
// Hardcover client's own Author records -- an Author with a spec.hardcoverID
// got Hardcover's rating and no logo. The lean hardcover.Client.Page, asked
// with the ids the document and the spec carry (target.go threads
// spec.hardcoverID beside the Open Library key), answers the page, and
// appended to the document's Links it renders through linksAC as the type
// ui/author.go's providerLinksOf reads, after Open Library's own links.
// Both documents come through the real clients over the recorded fixtures.
// The gateway's call (enrich.go, after the primary lookup, when the Links
// name no "hardcover" page) is the PageProvider leftover this test is the
// status half of.
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
	hc := hardcoverFixtures(t, map[string]string{"AuthorPageByID": "author_page_51942.json"})

	// The spec's ids as target.go builds them: the Open Library key the CR
	// is keyed by, and the hardcover id beside it.
	specIDs := pkgmetadata.ExternalIDs{pkgmetadata.KeyOpenLibraryAuthor: "OL21594A", extid.KeyHardcover: "51942"}
	a, err := primary.Author(context.Background(), specIDs)
	require.NoError(t, err)
	require.Equal(t, "Jane Austen", a.Name)
	for _, l := range a.Links {
		require.NotEqual(t, "hardcover", l.Type, "Open Library records no Hardcover page: the primary's document is the one without the link")
	}

	// enrich merges the spec's ids into the document's before it asks the
	// supplementary providers; the same ids address the page.
	a.IDs = a.IDs.Merge(specIDs)
	link, err := hc.Page(context.Background(), commonv1.MediaKindAuthor, a.IDs)
	require.NoError(t, err)
	require.Equal(t, pkgmetadata.Link{Type: "hardcover", URL: "https://hardcover.app/authors/sharon-m-draper"}, link)
	a.Links = append(a.Links, link)

	ac := buildAuthorMetadataAC(a, nil, "", now)
	var got []catalogv1alpha1.Link
	for _, l := range ac.Links {
		got = append(got, catalogv1alpha1.Link{Type: *l.Type, URL: *l.URL})
	}
	require.Contains(t, got, catalogv1alpha1.Link{Type: "hardcover", URL: "https://hardcover.app/authors/sharon-m-draper"}, "the type the author page's providerLinksOf reads")
	require.Equal(t, "hardcover", *ac.Links[len(ac.Links)-1].Type, "appended after the primary's own links")
	require.Equal(t, "51942", ac.ExternalIDs["hardcover"], "the id that addressed the page rides in externalIDs too")
}
