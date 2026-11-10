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

package ui_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// An issue's page is the movie page for one issue: the hero on the comic's
// background with the issue's cover (else the comic's poster), the title,
// the comic and number, the dates, credits and logos, the facts, the
// overview, then the Files table in the issue's columns and the
// neighbouring issues. What belongs to the comic stays on its page.
func TestIssuePageShowsWhatTheEpisodePageShows(t *testing.T) {
	srv, _ := comicFixture(t)
	rec := getPath(t, srv, "/library/default/issue/saga-001.0", false)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "private, max-age=10", rec.Header().Get("Cache-Control"), "the title preloads the page")
	page := rec.Body.String()
	require.Contains(t, page, "<html")
	requireTag(t, page, `data-issue-page="saga-001.0"`, `data-kind="issue"`, `data-number="1"`, `data-monitored="true"`,
		`data-hasfile="true"`, `data-status="file"`, `data-profile="comic"`)

	// The hero.
	require.Contains(t, page, "<title>Saga - #1 - Chapter One")
	require.Regexp(t, `<h1[^>]*>Chapter One</h1>`, page)
	require.Regexp(t, `href="/library/default/comic/saga"[^>]*data-comic-link[^>]*>Saga<`, page)
	require.Regexp(t, `data-issue-code[^>]*>#1<`, page)
	require.Contains(t, page, `<time datetime="2012-03-14" title="2012-03-14" data-cover-date>Cover Mar 14 2012</time>`)
	require.Contains(t, page, `<time datetime="2012-03-14" title="2012-03-14" data-store-date>In shops Mar 14 2012</time>`)
	require.Regexp(t, `data-backdrop src="/art/comic/uid-saga/fanart\?v=f4n4rt"`, page, "the comic's background")
	require.Regexp(t, `data-cover src="/art/search\?[^"]*"`, page, "the issue's ComicVine cover, through the signed proxy (comicvine.gamespot.com is an allow-listed host since 2026-11-10)")
	require.Contains(t, page, `href="https://comicvine.gamespot.com/issue/4000-12345/"`, "the issue's own ComicVine page")
	require.Contains(t, page, `<img src="/static/providers/comicvine.svg" alt="ComicVine"`)
	comic := section(t, page, `data-comic-links`)
	require.Contains(t, comic, ">Comic<")
	require.Contains(t, comic, `href="https://comicvine.gamespot.com/volume/4050-56582/"`)
	require.Contains(t, comic, `title="ComicVine (comic)"`)
	require.Less(t, strings.Index(page, `alt="ComicVine"`), strings.Index(page, `data-comic-links`), "the issue's own link comes first")
	require.Contains(t, page, "Marko and Alana flee with their newborn daughter.")
	require.NotContains(t, page, "Two soldiers from opposite sides", "the comic's overview is its own page's")
	require.Regexp(t, `data-credit-role="writer"[^>]*>Brian K. Vaughan \(writer\)<`, page)
	require.Regexp(t, `data-credit-role="artist"[^>]*>Fiona Staples \(artist\)<`, page)
	for fact, want := range map[string]string{
		"path":      "/data/comics/Saga (2012)",
		"status":    "Cutoff Unmet",
		"profile":   "comic",
		"size":      "40.00 MiB",
		"publisher": "Image Comics",
		"credits":   "Brian K. Vaughan",
		"language":  "English",
		"genres":    "Science Fiction, Fantasy",
	} {
		require.Regexp(t, `data-fact="`+fact+`"[\s\S]*?`+regexp.QuoteMeta(want), page, fact)
	}
	requireTag(t, page, `data-status="cutoff-unmet"`, `bg-amber-500`)

	// The bookmark and the toolbar, each returning here; the interactive
	// search opens the modal in the page's own #issue-modal.
	issueURL := "/library/default/issue/saga-001.0"
	require.Contains(t, page, `action="`+issueURL+`/monitor"`)
	require.Contains(t, page, `name="monitored" value="false"`)
	require.Contains(t, page, `action="`+issueURL+`/search"`)
	require.Regexp(t, `hx-post="`+issueURL+`/search/interactive"[^>]*hx-target="#issue-modal"`, page)
	require.Contains(t, page, `action="/library/default/comic/saga/refresh"`)
	require.Contains(t, page, `name="return" value="`+issueURL+`"`)
	require.Contains(t, page, "Refresh &amp; Scan Comic")
	require.Contains(t, page, `id="issue-modal"`)
	require.Contains(t, page, `id="action-status"`)
	require.NotContains(t, page, `data-action="delete"`, "deleting is the comic's page's")
	require.NotContains(t, page, `data-action="rename-apply"`, "renaming is the comic's page's")

	// The Files table in the issue's columns.
	files := section(t, page, `data-section="files"`)
	requireTag(t, files, `data-file="saga-001.0-file"`)
	require.Contains(t, files, "Saga 001 (2012).cbz", "the path relative to the comic's folder")
	require.NotContains(t, files, "/data/comics/Saga (2012)/Saga 001 (2012).cbz")
	require.Regexp(t, `data-cell="format"[\s\S]*?>CBZ<`, files)
	require.Regexp(t, `data-cell="pages"[^>]*>44<`, files)
	require.Contains(t, files, ">Format<")
	require.Contains(t, files, ">Pages<")
	require.NotContains(t, files, ">Video Codec<", "an issue's file has no video columns")
	require.Contains(t, files, "Zone-Empire")

	// The neighbours: the first issue has no previous one.
	require.NotContains(t, page, `data-nav="prev"`)
	require.Regexp(t, `href="/library/default/issue/saga-002.0"[^>]*data-nav="next"`, page)

	// An issue without a file: missing, the comic's folder, its cover
	// through the proxy, both neighbours.
	missing := getPath(t, srv, "/library/default/issue/saga-004.0", false).Body.String()
	requireTag(t, missing, `data-issue-page="saga-004.0"`, `data-status="missing"`, `data-hasfile="false"`)
	require.Contains(t, missing, "No file on disk.")
	require.Regexp(t, `data-fact="path"[\s\S]*?/data/comics/Saga \(2012\)<`, missing)
	require.Regexp(t, `data-cover src="/art/search\?[^"]+"`, missing, "the cover, through the art proxy")
	require.Contains(t, missing, `href="https://comicvine.gamespot.com/issue/4000-23456/"`, "the sourceIDs' ComicVine id")
	require.Regexp(t, `href="/library/default/issue/saga-003.0"[^>]*data-nav="prev"`, missing)
	require.Regexp(t, `href="/library/default/issue/saga-005.0"[^>]*data-nav="next"`, missing)

	unreleased := getPath(t, srv, "/library/default/issue/saga-006.0", false).Body.String()
	requireTag(t, unreleased, `data-issue-page="saga-006.0"`, `data-status="unreleased"`)
	require.Contains(t, unreleased, ">In shops from ")

	untitled := getPath(t, srv, "/library/default/issue/saga-007.0", false).Body.String()
	require.Regexp(t, `<h1[^>]*>#7</h1>`, untitled)
	require.Contains(t, untitled, "<title>Saga - #7 ·")

	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/issue/nope", false).Code)
	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/issue/paper-girls-001.0", false).Code,
		"an issue whose comic the library does not hold")
}

// The details route serves the issue's modal to htmx -- the heading, the
// facts, the overview, the credits and the file, Details open, or Search
// open with ?tab=search -- and the issue's page to anyone else.
func TestIssueDetailsServesTheModalToHTMXAndThePageOtherwise(t *testing.T) {
	srv, _ := comicFixture(t)
	rec := getPath(t, srv, "/library/default/issue/saga-001.0/details", true)
	require.Equal(t, http.StatusOK, rec.Code)
	modal := rec.Body.String()
	require.NotContains(t, modal, "<html")
	require.Contains(t, modal, `data-issue-modal="saga-001.0"`)
	require.Contains(t, modal, "Saga - #1 - Chapter One")
	require.Contains(t, modal, "Cover Mar 14 2012 · In shops Mar 14 2012 · CBZ")
	require.Contains(t, modal, "Marko and Alana flee with their newborn daughter.")
	require.Contains(t, modal, `data-issue-credits="2"`)
	requireTag(t, modal, `data-issue-file="saga-001.0-file"`)
	require.Contains(t, modal, "Saga 001 (2012).cbz")
	require.Contains(t, modal, "40.00 MiB")
	requireTag(t, modal, `data-tab="details"`, `aria-selected="true"`)
	require.Contains(t, modal, `data-issue-search="saga-001.0"`)
	require.Contains(t, modal, "Automatic Search")
	require.Contains(t, modal, "Interactive Search")

	searchTab := getPath(t, srv, "/library/default/issue/saga-004.0/details?tab=search", true).Body.String()
	requireTag(t, searchTab, `data-tab="search"`, `aria-selected="true"`)
	require.Contains(t, searchTab, `data-issue-file=""`, "no file on disk")
	require.Contains(t, searchTab, `hx-select="#issue-search-body"`)
	require.Contains(t, searchTab, "Saga - #4 - Chapter Four")

	page := getPath(t, srv, "/library/default/issue/saga-001.0/details", false)
	require.Equal(t, http.StatusOK, page.Code)
	require.Contains(t, page.Body.String(), "<html")
	require.Contains(t, page.Body.String(), `data-issue-page="saga-001.0"`)

	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/issue/nope/details", true).Code)
}

// The row's interactive search creates a Search for the issue that grabs
// nothing and answers the modal on its Search tab, the results panel
// polling the shared /searches route, which draws an issue's Search in
// comic columns -- Format, Size, Age, Indexer, Peers, Score -- lists a
// settled search's releases with their format, and whose grab route
// requests a grab and answers the same columns. The issue's own panel and
// grab routes are gone. An unknown issue creates nothing.
func TestIssueInteractiveSearchOpensTheModalInComicColumns(t *testing.T) {
	srv, c := comicFixture(t)
	rec := postForm(t, srv, "/library/default/issue/saga-004.0/search/interactive", url.Values{}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	var list catalogv1.SearchList
	require.NoError(t, c.List(t.Context(), &list, client.InNamespace("default")))
	var created *catalogv1.Search
	for i := range list.Items {
		if list.Items[i].Name != "saga-004.0-abc12" {
			created = &list.Items[i]
		}
	}
	require.NotNil(t, created, "the interactive search created a Search")
	require.Equal(t, &commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "saga-004.0"}, created.Spec.MediaRef)
	require.False(t, created.Spec.GrabBest, "an interactive search grabs only what a person picks")

	require.Contains(t, body, `data-issue-modal="saga-004.0"`)
	requireTag(t, body, `data-tab="search"`, `aria-selected="true"`)
	requireTag(t, body, `id="issue-search-body"`, `data-issue-search="saga-004.0"`)
	requireTag(t, body, `data-search-panel="`+created.Name+`"`,
		`hx-get="/searches/default/`+created.Name+`"`, `hx-trigger="load delay:2s"`, `hx-swap="outerHTML"`)
	require.Contains(t, body, "Searching indexers")

	// The shared route, for the search already answered: Mylar's columns,
	// the format badge, no polling once settled.
	panelURL := "/searches/default/saga-004.0-abc12"
	rec = getPath(t, srv, panelURL, true)
	require.Equal(t, http.StatusOK, rec.Code)
	panel := rec.Body.String()
	require.NotContains(t, panel, "<html")
	tag := requireTag(t, panel, `data-search-panel="saga-004.0-abc12"`, `data-search-phase="Completed"`)
	require.NotContains(t, tag, "hx-get", "a settled search stops polling")
	require.Contains(t, panel, "2 results from 1 indexers")
	requireTag(t, panel, `data-release-table`, `data-release-columns="format"`)
	for _, head := range []string{">Source<", ">Age<", ">Title<", ">Format<", ">Indexer<", ">Size<", ">Peers<", ">Score<"} {
		require.Contains(t, panel, head)
	}
	require.NotContains(t, panel, ">Languages<", "comic columns, not Sonarr's")
	require.NotContains(t, panel, ">Quality<")
	requireTag(t, panel, `data-release="guid-best"`, `data-approved="true"`, `data-grab=""`)
	require.Regexp(t, `data-format="CBZ"[^>]*>[\s\S]*?>CBZ<`, panel)
	requireTag(t, panel, `data-release="guid-pdf"`, `data-approved="false"`)
	require.Contains(t, panel, "40.00 MiB")
	require.Less(t, strings.Index(panel, `data-release="guid-best"`), strings.Index(panel, `data-release="guid-pdf"`), "ranked order")
	requireTag(t, panel, `data-rejections="1"`, `title="Quality: PDF is not wanted in profile"`)
	require.Contains(t, panel, `hx-post="`+panelURL+`/grab"`, "the download button grabs through the shared route")
	require.Contains(t, panel, `hx-confirm="This release was rejected:`)

	// A form post lands on the panel's page, in the same columns.
	page := getPath(t, srv, panelURL, false)
	require.Equal(t, http.StatusOK, page.Code)
	require.Contains(t, page.Body.String(), "<html")
	require.Contains(t, page.Body.String(), "Interactive search: saga-004.0")
	require.Contains(t, page.Body.String(), `data-release-columns="format"`)

	// The grab route: the release is requested, the panel polls for its
	// outcome at the shared route, still in comic columns.
	rec = postForm(t, srv, panelURL+"/grab", url.Values{"guid": {"guid-best"}}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	grabbed := rec.Body.String()
	requireTag(t, grabbed, `data-release="guid-best"`, `data-grab="requested"`)
	requireTag(t, grabbed, `data-search-panel="saga-004.0-abc12"`, `hx-get="`+panelURL+`"`, `hx-trigger="load delay:2s"`)
	require.Contains(t, grabbed, `data-release-columns="format"`, "the grab keeps the columns")
	var search catalogv1.Search
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Namespace: "default", Name: "saga-004.0-abc12"}, &search))
	require.Equal(t, []string{"guid-best"}, search.Spec.Grab)

	// A release the search does not list fails inside the panel; a search
	// that is gone says so.
	rec = postForm(t, srv, panelURL+"/grab", url.Values{"guid": {"guid-nope"}}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `data-action-error="invalid"`)
	gone := getPath(t, srv, "/searches/default/expired", true).Body.String()
	require.Contains(t, gone, "This search has expired")
	tag = requireTag(t, gone, `data-search-panel="expired"`)
	require.NotContains(t, tag, "hx-get")

	// The issue's own panel and grab routes are gone (C1, 2026-11-10).
	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/issue/saga-004.0/search/saga-004.0-abc12", true).Code)
	require.Equal(t, http.StatusNotFound, postForm(t, srv, "/library/default/issue/saga-004.0/search/saga-004.0-abc12/grab", url.Values{"guid": {"guid-best"}}, true).Code)

	rec = postForm(t, srv, "/library/default/issue/nope/search/interactive", url.Values{}, true)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.NoError(t, c.List(t.Context(), &list, client.InNamespace("default")))
	require.Len(t, list.Items, 2, "the fixture's and the interactive search's; none for an unknown issue")

}
