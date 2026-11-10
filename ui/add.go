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

package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// defaultAddSearchTimeout bounds one Add New metadata search (spec: 10 s),
// and the author lookup an audiobook's add makes.
const defaultAddSearchTimeout = 10 * time.Second

// MetadataLookup is Options.MetadataLookup's type: one lookup of an item by
// its ids through catalog's metadata gateway, the same shape as
// MetadataSearch. cmd/ui binds it to rpc.catalog.metadata.lookup exactly as
// it binds the search, so the ui still holds no requester (ui/guard_test.go).
// Add New asks it for an Audible author by ASIN, the audiobook's author link
// (2026-11-10 design D11, §5.6; audiobookAuthorRef).
type MetadataLookup func(ctx context.Context, req schema.MetadataRequest) (schema.MetadataResponse, error)

// monitorNewAllOrNone is the monitor-new choice of a kind with the flag
// alone (a comic's spec.monitorNewIssues) or the two-mode enum (an
// author's spec.monitorNewItems).
var monitorNewAllOrNone = []views.AddOption{{Value: "all", Label: "All"}, {Value: "none", Label: "None"}}

// addKinds is Add New per library tab (docs/superpowers/specs/
// 2026-09-29-add-new-design.md; the Comics and Audiobooks tabs since the
// 2026-11-10 design §9.1): the kind it adds, the provider id key its hits
// carry, the profile media kind, and the kind's own monitor choices.
var addKinds = map[projection.Tab]views.AddKind{
	projection.TabMovies: {
		Tab: projection.TabMovies, Kind: commonv1.MediaKindMovie, Label: "Movie", RootKind: catalogv1.RootFolderKindMovie, IDKey: metadata.KeyTMDB,
		ProfileKind: catalogv1.ProfileMediaKindVideo,
		Monitor: []views.AddOption{
			{Value: "movieOnly", Label: "Movie only"},
			{Value: "movieAndCollection", Label: "Movie and collection"},
			{Value: "none", Label: "None"},
		},
		SearchOnAdd: "Start search for missing",
	},
	projection.TabTV: {
		Tab: projection.TabTV, Kind: commonv1.MediaKindSeries, Label: "Series", RootKind: catalogv1.RootFolderKindSeries, IDKey: metadata.KeyTVDB,
		ProfileKind: catalogv1.ProfileMediaKindVideo,
		// Every season but the specials first, so it is the default: a
		// monitored series monitors all of its seasons (owner, 2026-10-27).
		Monitor: []views.AddOption{
			{Value: "unmonitorSpecials", Label: "All seasons, no specials"},
			{Value: "all", Label: "All episodes"},
			{Value: "future", Label: "Future episodes"},
			{Value: "missing", Label: "Missing episodes"},
			{Value: "existing", Label: "Existing episodes"},
			{Value: "firstSeason", Label: "First season"},
			{Value: "lastSeason", Label: "Last season"},
			{Value: "pilot", Label: "Pilot"},
			{Value: "recent", Label: "Recent episodes"},
			{Value: "none", Label: "None"},
		},
		MonitorNew:  monitorNewAllOrNone,
		SearchOnAdd: "Start search for missing",
	},
	projection.TabMusic: {
		Tab: projection.TabMusic, Kind: commonv1.MediaKindArtist, Label: "Artist", RootKind: catalogv1.RootFolderKindMusic, IDKey: metadata.KeyMBArtist,
		ProfileKind: catalogv1.ProfileMediaKindMusic,
		Monitor: []views.AddOption{
			{Value: "all", Label: "All albums"},
			{Value: "future", Label: "Future albums"},
			{Value: "missing", Label: "Missing albums"},
			{Value: "existing", Label: "Existing albums"},
			{Value: "latest", Label: "Latest album"},
			{Value: "first", Label: "First album"},
			{Value: "none", Label: "None"},
		},
		MonitorNew:  []views.AddOption{{Value: "all", Label: "All"}, {Value: "none", Label: "None"}, {Value: "new", Label: "New"}},
		SearchOnAdd: "Start search for missing",
	},
	projection.TabBooks: {
		Tab: projection.TabBooks, Kind: commonv1.MediaKindAuthor, Label: "Author", RootKind: catalogv1.RootFolderKindBook, IDKey: metadata.KeyOpenLibraryAuthor,
		ProfileKind: catalogv1.ProfileMediaKindBook,
		Monitor: []views.AddOption{
			{Value: "all", Label: "All books"},
			{Value: "future", Label: "Future books"},
			{Value: "missing", Label: "Missing books"},
			{Value: "existing", Label: "Existing books"},
			{Value: "none", Label: "None"},
		},
		MonitorNew:  monitorNewAllOrNone,
		SearchOnAdd: "Start search for missing",
	},
	// A comic's hits come from whichever comic provider answers (the
	// metadata gateway asks its ComicProviders in priority order; a
	// ComicVine hit carries a comicvine id, a MangaDex one a mangadex id),
	// so the hit's id key says the source the Comic is created with. Each
	// key is spelled as the source, which is how the clients key them
	// (metadata.KeyComicVine; extid.KeyMangaDex, which the ui binary never
	// links -- TestMangaDexHitsAreKeyedAsTheClientKeysThem holds the
	// spelling).
	projection.TabComics: {
		Tab: projection.TabComics, Kind: commonv1.MediaKindComic, Label: "Comic", RootKind: catalogv1.RootFolderKindComic,
		IDKey: metadata.KeyComicVine,
		IDKeys: []views.AddIDKey{
			{Key: metadata.KeyComicVine, Source: string(catalogv1.ComicSourceComicVine), Label: "ComicVine"},
			{Key: string(catalogv1.ComicSourceMangaDex), Source: string(catalogv1.ComicSourceMangaDex), Label: "MangaDex"},
		},
		ProfileKind: catalogv1.ProfileMediaKindComic,
		Monitor: []views.AddOption{
			{Value: "all", Label: "All issues"},
			{Value: "future", Label: "Future issues"},
			{Value: "missing", Label: "Missing issues"},
			{Value: "existing", Label: "Existing issues"},
			{Value: "none", Label: "None"},
		},
		MonitorNew:  monitorNewAllOrNone,
		SearchOnAdd: "Start search for missing issues",
	},
	projection.TabAudiobooks: {
		Tab: projection.TabAudiobooks, Kind: commonv1.MediaKindAudiobook, Label: "Audiobook", RootKind: catalogv1.RootFolderKindAudiobook,
		IDKey: metadata.KeyASIN, ProfileKind: catalogv1.ProfileMediaKindAudiobook,
		SearchOnAdd: "Start search",
	},
}

// altAddKinds are a tab's further Add New kinds, reached with ?kind=<kind>
// (views.AddKind.Alternate): the Books tab adds an Author, or a standalone
// Book -- an Open Library work with no author, its own profile and root
// folder (2026-11-10 design §9.1, §9.3). A Book has no add options, so
// its form offers the root, the profile and monitored alone.
var altAddKinds = map[projection.Tab][]views.AddKind{
	projection.TabBooks: {{
		Tab: projection.TabBooks, Kind: commonv1.MediaKindBook, Label: "Book", RootKind: catalogv1.RootFolderKindBook, IDKey: metadata.KeyOpenLibraryWork,
		ProfileKind: catalogv1.ProfileMediaKindBook, Alternate: true,
	}},
}

// searchHit decodes one search result: a MovieHit's lower-case keys and a
// SearchHit's field names alike, since encoding/json matches keys without
// regard to case. The kind-specific fields are metadata.SearchHit's (task
// C4a): a comic volume's publisher, issue count and start year (2026-11-10
// design §9.4), an audiobook's authors -- each with the Audible author's
// ASIN the author link looks up (D11) -- narrators and release date (§9.5),
// and a manga's creators under authors; every one empty for a kind that
// has none.
type searchHit struct {
	IDs    map[string]string `json:"ids"`
	Title  string            `json:"title"`
	Year   int32             `json:"year"`
	Poster string            `json:"poster"`

	Publisher   string      `json:"publisher"`
	IssueCount  int32       `json:"issueCount"`
	StartYear   *int32      `json:"startYear"`
	Authors     []hitPerson `json:"authors"`
	Narrators   []string    `json:"narrators"`
	ReleaseDate *time.Time  `json:"releaseDate"`
}

// hitPerson is one credited person of a hit (metadata.NamedRef): an
// audiobook's author with the ASIN of their Audible page, a manga's creator
// with none.
type hitPerson struct {
	Name string `json:"name"`
	ASIN string `json:"asin"`
}

// addKind is the kind the request's tab adds: the tab's own, or the
// alternate its "kind" (a query on a GET, a hidden field on the POST)
// names.
func (s *Server) addKind(r *http.Request) (views.AddKind, bool) {
	tab := projection.Tab(r.PathValue("tab"))
	k, ok := addKinds[tab]
	if !ok {
		return views.AddKind{}, false
	}
	if want := r.FormValue("kind"); want != "" && want != string(k.Kind) {
		for _, alt := range altAddKinds[tab] {
			if string(alt.Kind) == want {
				return alt, true
			}
		}
		return views.AddKind{}, false
	}
	return k, true
}

// tabKinds is every kind a tab's Add New adds, its own first: the page's
// kind switch when there are two.
func tabKinds(tab projection.Tab) []views.AddKind {
	k, ok := addKinds[tab]
	if !ok {
		return nil
	}
	return append([]views.AddKind{k}, altAddKinds[tab]...)
}

// addChoices lists the root folders of k's own kind -- a book root for an
// Author, never an audiobook or comic one, which the rescan would read as
// its own kind -- each valued "namespace/name" so the item is created
// beside its root folder, and the quality profiles of k's media kind.
func (s *Server) addChoices(ctx context.Context, k views.AddKind) (roots, profiles []views.AddOption) {
	for _, rf := range s.listRootFolders(ctx) {
		if rf.Spec.Kind == k.RootKind {
			roots = append(roots, views.AddOption{Value: rf.Namespace + "/" + rf.Name, Label: rf.Name + " (" + rf.Spec.Path + ")"})
		}
	}
	for _, qp := range s.listQualityProfiles(ctx) {
		if qp.Spec.MediaKind == k.ProfileKind {
			profiles = append(profiles, views.AddOption{Value: qp.Name, Label: qp.Name})
		}
	}
	return roots, profiles
}

// addRegion is the Audible marketplace an audiobook search asks in and an
// added audiobook takes (2026-11-10 design §4.8, §9.5): the first
// audiobook root's defaults.region, by name, else us. Every other kind has
// none.
func (s *Server) addRegion(ctx context.Context, k views.AddKind) string {
	if k.Kind != commonv1.MediaKindAudiobook {
		return ""
	}
	for _, rf := range s.listRootFolders(ctx) {
		if rf.Spec.Kind == k.RootKind && rf.Spec.Defaults.Region != "" {
			return string(rf.Spec.Defaults.Region)
		}
	}
	return string(catalogv1.AudiobookRegionUS)
}

// inLibrary maps each provider id of k's kind in the library to the
// item's detail path. The ids are the projection's ProviderIDs: a comic's
// "<source>:<id>", an audiobook's "asin:<id>", a book's "olid:<id>"
// (providerKey), a movie's bare TMDB id.
func (s *Server) inLibrary(ctx context.Context, k views.AddKind) map[string]string {
	out := map[string]string{}
	if s.opts.Library == nil {
		return out
	}
	for _, it := range s.opts.Library(ctx) {
		if it.Kind == k.Kind && it.ProviderID != "" {
			out[it.ProviderID] = "/library/" + it.Ref.Namespace + "/" + string(it.Kind) + "/" + it.Ref.Name
		}
	}
	return out
}

// providerKey is the projection's ProviderID for a hit of k's kind, the
// key inLibrary's map carries (ui/projection.describeLibraryItem).
func providerKey(k views.AddKind, source, id string) string {
	switch k.Kind {
	case commonv1.MediaKindComic:
		return source + ":" + id
	case commonv1.MediaKindAudiobook:
		return projection.ProviderIDASIN + ":" + id
	case commonv1.MediaKindBook:
		return projection.ProviderIDOpenLibrary + ":" + id
	}
	return id
}

func (s *Server) handleAddPage(w http.ResponseWriter, r *http.Request) {
	k, ok := s.addKind(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// ?q fills the box and searches at once: the library typeahead's
	// "Add a new …" carries what the reader typed (2026-10-01).
	s.renderAddPage(w, r, k, http.StatusOK, views.AddPageData{Query: strings.TrimSpace(r.URL.Query().Get("q"))})
}

// renderAddPage renders the Add New page from d, which carries the error
// and the owner's search and form after a rejected add.
func (s *Server) renderAddPage(w http.ResponseWriter, r *http.Request, k views.AddKind, status int, d views.AddPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	d.Kind, d.Kinds, d.Available = k, tabKinds(k.Tab), s.opts.MetadataSearch != nil
	if err := views.AddPage(d).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render add page", "error", err)
	}
}

func (s *Server) handleAddSearch(w http.ResponseWriter, r *http.Request) {
	k, ok := s.addKind(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	res := views.AddResultsData{Kind: k, Query: q, Region: s.addRegion(r.Context(), k)}
	switch {
	case utf8.RuneCountInString(q) < 2:
		res.Prompt = true
	case s.opts.MetadataSearch == nil:
		res.Error = "Metadata search is not available: the ui has no connection to the catalog metadata service."
	default:
		s.addSearch(r.Context(), k, q, &res)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.AddResults(res).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render add results", "error", err)
	}
}

// sourceAnswer is one metadata search's answer: the source it asked for
// (a comic's ComicVine or MangaDex; the zero AddIDKey for a kind with one
// provider set), the gateway's response and the request's own error.
type sourceAnswer struct {
	source views.AddIDKey
	resp   schema.MetadataResponse
	err    error
}

// addSearch runs k's metadata search for q into res: one request for a kind
// whose gateway search asks its providers in priority order, and for a
// kind with several id keys (a comic, design §9.4) one request per source,
// MetadataRequest.Source naming it (task C4a), run at once under the one
// search deadline and merged in the keys' order -- ComicVine's volumes,
// then MangaDex's manga, each hit badged with its source -- so a manga
// appears beside the volumes where the gateway's unnamed search answered
// from the first provider alone.
//
// A source no provider is configured for is skipped as long as another
// answered; every source unconfigured is the "no provider" prompt. A source
// that failed or did not answer is logged and skipped while another
// answered -- a reader sees ComicVine's hits when MangaDex is down -- and
// says so, as the one search did, only when none did.
func (s *Server) addSearch(ctx context.Context, k views.AddKind, q string, res *views.AddResultsData) {
	// The searches run under the one deadline; the hits' library check and
	// the form's choices read under the request's own, so a source that
	// timed out never costs the hits another source answered their root
	// folders and profiles.
	searchCtx, cancel := context.WithTimeout(ctx, s.addSearchTimeout)
	defer cancel()
	sources := k.IDKeys
	if len(sources) == 0 {
		sources = []views.AddIDKey{{}}
	}
	answers := make([]sourceAnswer, len(sources))
	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := s.opts.MetadataSearch(searchCtx, schema.MetadataRequest{Kind: k.Kind, Text: q, Region: res.Region, Source: src.Source})
			answers[i] = sourceAnswer{source: src, resp: resp, err: err}
		}()
	}
	wg.Wait()

	var (
		hits      []views.AddHit
		answered  bool
		firstFail *sourceAnswer
	)
	for i := range answers {
		a := &answers[i]
		switch {
		case a.err == nil && a.resp.Error == "":
			answered = true
			hits = append(hits, s.addHits(ctx, k, a.source.Source, a.resp.Results)...)
		case a.err == nil && noProviderConfigured(a.resp.Error):
			// An unconfigured source is skipped; the "no provider" prompt
			// follows below from no source having answered or failed.
		default:
			if firstFail == nil {
				firstFail = a
			}
			logging.FromContext(ctx).Warn("add new: metadata search failed", "kind", k.Kind, "source", a.source.Source, "error", a.err, "response", a.resp.Error)
		}
	}
	switch {
	case answered:
		res.Hits = hits
		res.RootFolders, res.Profiles = s.addChoices(ctx, k)
	case firstFail == nil:
		res.NoProvider = true
	case firstFail.err != nil && (errors.Is(firstFail.err, context.DeadlineExceeded) || errors.Is(firstFail.err, events.ErrNoResponders) || searchCtx.Err() != nil):
		// Slow, or down: NATS answers "no responders" at once when
		// nothing serves the subject.
		res.Error = "Metadata search is not responding."
		res.RetryURL = views.AddSearchURL(k, q)
	case firstFail.err != nil:
		res.Error = clampMessage("Metadata search failed: " + firstFail.err.Error())
	default:
		res.Error = clampMessage(firstFail.resp.Error)
	}
}

// noProviderConfigured reads the gateway's "no <kind> metadata provider is
// configured to search" and its source-named form, "no <kind> metadata
// provider of type <source> is configured to search" (task C4a).
func noProviderConfigured(msg string) bool {
	return strings.Contains(msg, "metadata provider") && strings.Contains(msg, "is configured to search")
}

// addHits decodes the search results, drops a hit with no id for k, and
// marks one already in the library with its detail path. A kind with
// several id keys (a comic's ComicVine or MangaDex id) reads the key of
// the source the search asked for, so a hit MangaDex answered is a MangaDex
// comic whatever other ids it names; a search that named no source takes
// the first key a hit carries, which also names the hit's source.
func (s *Server) addHits(ctx context.Context, k views.AddKind, source string, results [][]byte) []views.AddHit {
	library := s.inLibrary(ctx, k)
	hits := make([]views.AddHit, 0, len(results))
	for _, raw := range results {
		var h searchHit
		if json.Unmarshal(raw, &h) != nil {
			continue
		}
		id, src, sourceLabel := hitID(k, source, h.IDs)
		if id == "" {
			continue
		}
		hit := views.AddHit{
			ID: id, Source: src, SourceLabel: sourceLabel, Title: h.Title, Year: h.Year,
			Poster: s.searchArt.URL(h.Poster), InLibrary: library[providerKey(k, src, id)],
			Publisher: h.Publisher, IssueCount: h.IssueCount, Narrators: h.Narrators,
		}
		if hit.Year == 0 && h.StartYear != nil {
			hit.Year = *h.StartYear
		}
		if h.ReleaseDate != nil {
			hit.Release = h.ReleaseDate.UTC().Format("2 Jan 2006")
		}
		for _, p := range h.Authors {
			if p.Name != "" {
				hit.Authors = append(hit.Authors, views.AddPerson{Name: p.Name, ASIN: p.ASIN})
			}
		}
		hits = append(hits, hit)
	}
	return hits
}

// hitID is the id a hit carries for k, and for a kind with several id keys
// the source and its label the key names: the key of source when the
// search named one, else the first key the hit carries; "" when the hit
// has none.
func hitID(k views.AddKind, source string, ids map[string]string) (id, src, label string) {
	if len(k.IDKeys) == 0 {
		return ids[k.IDKey], "", ""
	}
	for _, key := range k.IDKeys {
		if source != "" && key.Source != source {
			continue
		}
		if id := ids[key.Key]; id != "" {
			return id, key.Source, key.Label
		}
	}
	return "", "", ""
}

// clampMessage keeps a provider's error to a line a page can show.
func clampMessage(s string) string {
	const limit = 300
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit]) + "…"
}

func (s *Server) handleAddCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		if k, ok := addKinds[projection.Tab(r.PathValue("tab"))]; ok {
			s.renderAddPage(w, r, k, http.StatusBadRequest, views.AddPageData{ErrorCode: "invalid", Error: "The form could not be read."})
			return
		}
		http.NotFound(w, r)
		return
	}
	k, ok := s.addKind(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	id, source := r.PostFormValue("id"), r.PostFormValue("source")
	// The library may hold this item under another name -- a retitled
	// film, one a rescan named from its folder -- so it is looked up by
	// provider id first; the name only guards a concurrent add.
	if path, ok := s.inLibrary(r.Context(), k)[providerKey(k, source, id)]; ok && id != "" {
		http.Redirect(w, r, path, http.StatusSeeOther)
		return
	}
	// The posted root folder names the item's namespace, so it must be one
	// this page offers: a hand-made POST cannot pick any namespace, or a
	// root folder of another kind.
	posted := r.PostFormValue("rootFolder")
	roots, _ := s.addChoices(r.Context(), k)
	if !slices.ContainsFunc(roots, func(o views.AddOption) bool { return o.Value == posted }) {
		s.rejectAdd(w, r, k, fmt.Errorf("%w: %q is not a %s root folder", actions.ErrInvalid, posted, k.Label))
		return
	}
	ns, root, _ := strings.Cut(posted, "/")
	req := actions.AddRequest{
		Kind: k.Kind, Namespace: ns, Title: r.PostFormValue("title"), ProviderID: id, Source: source,
		RootFolderRef: root, QualityProfileRef: r.PostFormValue("qualityProfile"),
		Monitored: checked(r, "monitored"), Monitor: r.PostFormValue("monitor"),
		SearchOnAdd: checked(r, "searchOnAdd"), MinimumAvailability: r.PostFormValue("minimumAvailability"),
		SeriesType: r.PostFormValue("seriesType"), SeasonFolder: checked(r, "seasonFolder"),
		SearchCutoffUnmet: checked(r, "searchCutoffUnmet"), MonitorNewItems: r.PostFormValue("monitorNewItems"),
		ComicKind: r.PostFormValue("comicKind"), Region: r.PostFormValue("region"),
	}
	// The request is checked before the author link is resolved, so a form
	// AddItem refuses (no profile, an id that cannot key the kind) never
	// spends the lookup's gateway round trip.
	if err := actions.Validate(req); err != nil {
		s.rejectAdd(w, r, k, err)
		return
	}
	if k.Kind == commonv1.MediaKindAudiobook {
		req.AuthorRef = s.audiobookAuthorRef(r.Context(), ns, r.PostFormValue("authorName"), r.PostFormValue("authorASIN"), req.Region)
	}
	name, _, err := s.opts.Actions.AddItem(r.Context(), req)
	if err != nil {
		s.rejectAdd(w, r, k, err)
		return
	}
	http.Redirect(w, r, "/library/"+ns+"/"+string(k.Kind)+"/"+name, http.StatusSeeOther)
}

// audiobookAuthorRef is the Author an added audiobook is linked to
// (spec.authorRef; 2026-11-10 design D11, §5.6): the Author in ns whose
// name is the hit's first author's -- the form carries it as authorName --
// names compared through release.TitleNorm so case, punctuation and
// accents do not part "J.R.R. Tolkien" from "J. R. R. Tolkien". When the
// author has an Audible page (the form's authorASIN) it is also looked up
// through the metadata gateway (Options.MetadataLookup, Kind author with
// ids {asin, region}: Audnexus's /authors/{asin}, task C4a), and the name
// it answers is a second rendering of the same person to match by, since
// the providers spell one author differently ("frank herbert"). The form
// resolves no Book (Add New sets no bookRef), from whose author the ref
// would otherwise come.
//
// Exactly one Author links: the one both names match, or the one only one
// of them does. None, two (two Authors of one name, or the two names
// naming different Authors: never a guess) and a ui with no reader bound
// all link none, logged, and never fail the add: the Audiobook is still
// created, and its Edit dialog links an author by hand. A lookup that
// failed or did not answer, an author Audnexus has no page for
// (ErrNotFound on the wire: Tolkien's B000AQ1S22 answers 404), an answer
// with no name and a ui with no lookup bound leave the hit's own name to
// decide, as the design links by name alone.
func (s *Server) audiobookAuthorRef(ctx context.Context, ns, name, asin, region string) string {
	if (name == "" && asin == "") || s.opts.Reader == nil {
		return ""
	}
	log := logging.FromContext(ctx).With("author", name, "asin", asin, "namespace", ns)
	ctx, cancel := context.WithTimeout(ctx, s.addSearchTimeout)
	defer cancel()
	names := make([]string, 0, 2)
	if name != "" {
		names = append(names, name)
	}
	if looked := s.lookupAuthorName(ctx, log, asin, region); looked != "" {
		names = append(names, looked)
	}
	if len(names) == 0 {
		return ""
	}
	var list catalogv1.AuthorList
	if err := s.opts.Reader.List(ctx, &list, client.InNamespace(ns)); err != nil {
		log.Warn("add new: list authors; no author linked", "error", err)
		return ""
	}
	matches := authorsNamed(names, list.Items)
	switch len(matches) {
	case 1:
		return matches[0]
	case 0:
		log.Info("add new: no Author of the audiobook author's name; no author linked", "names", names)
	default:
		log.Info("add new: several Authors of the audiobook author's name; no author linked", "names", names, "authors", matches)
	}
	return ""
}

// lookupAuthorName is the name the metadata gateway's author lookup
// answers for an Audible author's ASIN in region, or "" when there is no
// ASIN or no lookup bound, or the lookup failed, answered an error or
// answered no name, each logged: the hit's own name is matched alone then.
func (s *Server) lookupAuthorName(ctx context.Context, log *slog.Logger, asin, region string) string {
	if asin == "" || s.opts.MetadataLookup == nil {
		return ""
	}
	ids := map[string]string{metadata.KeyASIN: asin}
	if region != "" {
		ids["region"] = region
	}
	resp, err := s.opts.MetadataLookup(ctx, schema.MetadataRequest{Kind: commonv1.MediaKindAuthor, IDs: ids})
	switch {
	case err != nil:
		log.Warn("add new: audiobook author lookup failed; the hit's name alone is matched", "error", err)
		return ""
	case resp.Error == metadata.ErrNotFound.Error():
		log.Info("add new: the audiobook's author has no provider page; the hit's name alone is matched")
		return ""
	case resp.Error != "":
		log.Warn("add new: audiobook author lookup failed; the hit's name alone is matched", "error", resp.Error)
		return ""
	}
	var author struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(resp.Result, &author); err != nil || author.Name == "" {
		log.Warn("add new: audiobook author lookup answered no name; the hit's name alone is matched", "error", err)
		return ""
	}
	return author.Name
}

// authorsNamed is the names of the Authors whose metadata name is one of
// names through release.TitleNorm, each Author once, in the list's order;
// an Author without metadata yet has no name to match.
func authorsNamed(names []string, authors []catalogv1.Author) []string {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		if norm := release.TitleNorm(n); norm != "" {
			want[norm] = true
		}
	}
	if len(want) == 0 {
		return nil
	}
	var out []string
	for i := range authors {
		md := authors[i].Status.Metadata
		if md == nil || md.Name == "" {
			continue
		}
		if want[release.TitleNorm(md.Name)] {
			out = append(out, authors[i].Name)
		}
	}
	return out
}

// checked reads a checkbox the add form posts as a hidden "false" followed
// by the box's "true" when it is ticked: any "true" means ticked.
func checked(r *http.Request, name string) bool {
	for _, v := range r.PostForm[name] {
		if v == "true" {
			return true
		}
	}
	return false
}

// justAdded reports whether the cluster holds the Add New kind item
// namespace/name that the library view does not show yet: the view is
// rebuilt every few seconds, and an add redirects to its item at once.
func (s *Server) justAdded(ctx context.Context, namespace, kind, name string) bool {
	if s.opts.Reader == nil {
		return false
	}
	var obj client.Object
	switch commonv1.MediaKind(kind) {
	case commonv1.MediaKindMovie:
		obj = &catalogv1.Movie{}
	case commonv1.MediaKindSeries:
		obj = &catalogv1.Series{}
	case commonv1.MediaKindArtist:
		obj = &catalogv1.Artist{}
	case commonv1.MediaKindAuthor:
		obj = &catalogv1.Author{}
	case commonv1.MediaKindComic:
		obj = &catalogv1.Comic{}
	case commonv1.MediaKindAudiobook:
		obj = &catalogv1.Audiobook{}
	case commonv1.MediaKindBook:
		obj = &catalogv1.Book{}
	default:
		return false
	}
	return s.opts.Reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, obj) == nil
}

// notFoundUnlessJustAdded answers a page whose item the library view does
// not list: Add New's pending page, which reloads, when the object is
// already in the cluster (the view rebuilds within a round), else 404. Every
// kind Add New creates goes through it, so an item just created never 404s
// on the redirect that follows (W4c's leftover, 2026-11-10); a Book's page
// asks only for a standalone one (renderBookXPage), since an author's book
// is the fan-out's, never Add New's.
func (s *Server) notFoundUnlessJustAdded(w http.ResponseWriter, r *http.Request, namespace, kind, name string) {
	if s.justAdded(r.Context(), namespace, kind, name) {
		s.renderAddPending(w, r, name)
		return
	}
	http.NotFound(w, r)
}

// renderAddPending is Add New's "being added" page, which reloads itself.
func (s *Server) renderAddPending(w http.ResponseWriter, r *http.Request, name string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.AddPending(name).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render add pending page", "error", err)
	}
}

// rejectAdd re-renders the Add New page with err and the owner's search and
// form as they submitted them, the rejected hit's form open (spec: Errors).
func (s *Server) rejectAdd(w http.ResponseWriter, r *http.Request, k views.AddKind, err error) {
	code, status := actionErrorCode(err)
	logging.FromContext(r.Context()).Error("add rejected", "kind", k.Kind, "error", err, "code", code)
	id, q := r.PostFormValue("id"), r.PostFormValue("q")
	hit := views.AddHit{ID: id, Source: r.PostFormValue("source"), Title: r.PostFormValue("title")}
	if name, asin := r.PostFormValue("authorName"), r.PostFormValue("authorASIN"); name != "" || asin != "" {
		// An audiobook's author rides the form again, so a second post
		// still links the Author.
		hit.Authors = []views.AddPerson{{Name: name, ASIN: asin}}
	}
	res := &views.AddResultsData{
		Kind: k, Query: q, OpenID: id, Posted: r.PostForm, Region: s.addRegion(r.Context(), k),
		Hits: []views.AddHit{hit},
	}
	res.RootFolders, res.Profiles = s.addChoices(r.Context(), k)
	s.renderAddPage(w, r, k, status, views.AddPageData{ErrorCode: code, Error: err.Error(), Query: q, Results: res})
}
