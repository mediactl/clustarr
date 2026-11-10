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
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/extid"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// defaultAddSearchTimeout bounds one Add New metadata search (spec: 10 s).
const defaultAddSearchTimeout = 10 * time.Second

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
	// so the hit's id key says the source the Comic is created with.
	projection.TabComics: {
		Tab: projection.TabComics, Kind: commonv1.MediaKindComic, Label: "Comic", RootKind: catalogv1.RootFolderKindComic,
		IDKey: metadata.KeyComicVine,
		IDKeys: []views.AddIDKey{
			{Key: metadata.KeyComicVine, Source: string(catalogv1.ComicSourceComicVine), Label: "ComicVine"},
			{Key: extid.KeyMangaDex, Source: string(catalogv1.ComicSourceMangaDex), Label: "MangaDex"},
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
// regard to case.
type searchHit struct {
	IDs    map[string]string `json:"ids"`
	Title  string            `json:"title"`
	Year   int32             `json:"year"`
	Poster string            `json:"poster"`
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
		ctx, cancel := context.WithTimeout(r.Context(), s.addSearchTimeout)
		defer cancel()
		resp, err := s.opts.MetadataSearch(ctx, schema.MetadataRequest{Kind: k.Kind, Text: q, Region: res.Region})
		switch {
		case err != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, events.ErrNoResponders) || ctx.Err() != nil):
			// Slow, or down: NATS answers "no responders" at once when
			// nothing serves the subject.
			res.Error = "Metadata search is not responding."
			res.RetryURL = views.AddSearchURL(k, q)
		case err != nil:
			res.Error = clampMessage("Metadata search failed: " + err.Error())
		case strings.Contains(resp.Error, "metadata provider is configured"):
			res.NoProvider = true
		case resp.Error != "":
			res.Error = clampMessage(resp.Error)
		default:
			res.Hits = s.addHits(r.Context(), k, resp.Results)
			res.RootFolders, res.Profiles = s.addChoices(r.Context(), k)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.AddResults(res).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render add results", "error", err)
	}
}

// addHits decodes the search results, drops a hit with no id for k, and
// marks one already in the library with its detail path. A kind with
// several id keys (a comic's ComicVine or MangaDex id) takes the first a
// hit carries, which also names the hit's source.
func (s *Server) addHits(ctx context.Context, k views.AddKind, results [][]byte) []views.AddHit {
	library := s.inLibrary(ctx, k)
	hits := make([]views.AddHit, 0, len(results))
	for _, raw := range results {
		var h searchHit
		if json.Unmarshal(raw, &h) != nil {
			continue
		}
		id, source, sourceLabel := hitID(k, h.IDs)
		if id == "" {
			continue
		}
		hits = append(hits, views.AddHit{
			ID: id, Source: source, SourceLabel: sourceLabel, Title: h.Title, Year: h.Year,
			Poster: s.searchArt.URL(h.Poster), InLibrary: library[providerKey(k, source, id)],
		})
	}
	return hits
}

// hitID is the id a hit carries for k, and for a kind with several id keys
// the source and its label the key names; "" when the hit has none.
func hitID(k views.AddKind, ids map[string]string) (id, source, label string) {
	if len(k.IDKeys) == 0 {
		return ids[k.IDKey], "", ""
	}
	for _, key := range k.IDKeys {
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
	name, _, err := s.opts.Actions.AddItem(r.Context(), req)
	if err != nil {
		s.rejectAdd(w, r, k, err)
		return
	}
	http.Redirect(w, r, "/library/"+ns+"/"+string(k.Kind)+"/"+name, http.StatusSeeOther)
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

// rejectAdd re-renders the Add New page with err and the owner's search and
// form as they submitted them, the rejected hit's form open (spec: Errors).
func (s *Server) rejectAdd(w http.ResponseWriter, r *http.Request, k views.AddKind, err error) {
	code, status := actionErrorCode(err)
	logging.FromContext(r.Context()).Error("add rejected", "kind", k.Kind, "error", err, "code", code)
	id, q := r.PostFormValue("id"), r.PostFormValue("q")
	res := &views.AddResultsData{
		Kind: k, Query: q, OpenID: id, Posted: r.PostForm, Region: s.addRegion(r.Context(), k),
		Hits: []views.AddHit{{ID: id, Source: r.PostFormValue("source"), Title: r.PostFormValue("title")}},
	}
	res.RootFolders, res.Profiles = s.addChoices(r.Context(), k)
	s.renderAddPage(w, r, k, status, views.AddPageData{ErrorCode: code, Error: err.Error(), Query: q, Results: res})
}
