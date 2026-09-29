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
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// defaultAddSearchTimeout bounds one Add New metadata search (spec: 10 s).
const defaultAddSearchTimeout = 10 * time.Second

// addKinds is Add New per library tab (docs/superpowers/specs/
// 2026-09-29-add-new-design.md): the kind it adds, the provider id key its
// hits carry, the profile media kind, and the kind's own monitor choices.
var addKinds = map[projection.Tab]views.AddKind{
	projection.TabMovies: {
		Tab: projection.TabMovies, Kind: commonv1.MediaKindMovie, Label: "Movie", RootKind: catalogv1.RootFolderKindMovie, IDKey: metadata.KeyTMDB,
		ProfileKind: catalogv1.ProfileMediaKindVideo,
		Monitor: []views.AddOption{
			{Value: "movieOnly", Label: "Movie only"},
			{Value: "movieAndCollection", Label: "Movie and collection"},
			{Value: "none", Label: "None"},
		},
	},
	projection.TabTV: {
		Tab: projection.TabTV, Kind: commonv1.MediaKindSeries, Label: "Series", RootKind: catalogv1.RootFolderKindSeries, IDKey: metadata.KeyTVDB,
		ProfileKind: catalogv1.ProfileMediaKindVideo,
		Monitor: []views.AddOption{
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
		MonitorNew: []views.AddOption{{Value: "all", Label: "All"}, {Value: "none", Label: "None"}},
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
		MonitorNew: []views.AddOption{{Value: "all", Label: "All"}, {Value: "none", Label: "None"}, {Value: "new", Label: "New"}},
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
		MonitorNew: []views.AddOption{{Value: "all", Label: "All"}, {Value: "none", Label: "None"}},
	},
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

func (s *Server) addKind(r *http.Request) (views.AddKind, bool) {
	k, ok := addKinds[projection.Tab(r.PathValue("tab"))]
	return k, ok
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

// inLibrary maps each provider id of k's kind in the library to the
// item's detail path.
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

func (s *Server) handleAddPage(w http.ResponseWriter, r *http.Request) {
	k, ok := s.addKind(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.renderAddPage(w, r, k, http.StatusOK, "", "")
}

func (s *Server) renderAddPage(w http.ResponseWriter, r *http.Request, k views.AddKind, status int, code, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	d := views.AddPageData{Kind: k, Available: s.opts.MetadataSearch != nil, ErrorCode: code, Error: errMsg}
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
	res := views.AddResultsData{Kind: k, Query: q}
	switch {
	case utf8.RuneCountInString(q) < 2:
		res.Prompt = true
	case s.opts.MetadataSearch == nil:
		res.Error = "Metadata search is not available: the ui has no connection to catalogarr."
	default:
		ctx, cancel := context.WithTimeout(r.Context(), s.addSearchTimeout)
		defer cancel()
		resp, err := s.opts.MetadataSearch(ctx, schema.MetadataRequest{Kind: k.Kind, Text: q})
		switch {
		case err != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, events.ErrNoResponders) || ctx.Err() != nil):
			// Slow, or down: NATS answers "no responders" at once when
			// nothing serves the subject.
			res.Error = "Metadata search is not responding."
			res.RetryURL = "/library/" + string(k.Tab) + "/add/search?q=" + url.QueryEscape(q)
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
// marks one already in the library with its detail path.
func (s *Server) addHits(ctx context.Context, k views.AddKind, results [][]byte) []views.AddHit {
	library := s.inLibrary(ctx, k)
	hits := make([]views.AddHit, 0, len(results))
	for _, raw := range results {
		var h searchHit
		if json.Unmarshal(raw, &h) != nil {
			continue
		}
		id := h.IDs[k.IDKey]
		if id == "" {
			continue
		}
		hits = append(hits, views.AddHit{ID: id, Title: h.Title, Year: h.Year, Poster: s.searchArt.URL(h.Poster), InLibrary: library[id]})
	}
	return hits
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
	k, ok := s.addKind(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderAddPage(w, r, k, http.StatusBadRequest, "invalid", "The form could not be read.")
		return
	}
	id := r.PostFormValue("id")
	// The library may hold this item under another name -- a retitled
	// film, one a rescan named from its folder -- so it is looked up by
	// provider id first; the name only guards a concurrent add.
	if path, ok := s.inLibrary(r.Context(), k)[id]; ok && id != "" {
		http.Redirect(w, r, path, http.StatusSeeOther)
		return
	}
	// The posted root folder names the item's namespace, so it must be one
	// this page offers: a hand-made POST cannot pick any namespace, or a
	// root folder of another kind.
	posted := r.PostFormValue("rootFolder")
	roots, _ := s.addChoices(r.Context(), k)
	if !slices.ContainsFunc(roots, func(o views.AddOption) bool { return o.Value == posted }) {
		err := fmt.Errorf("%w: %q is not a %s root folder", actions.ErrInvalid, posted, k.Label)
		code, status := actionErrorCode(err)
		s.renderAddPage(w, r, k, status, code, err.Error())
		return
	}
	ns, root, _ := strings.Cut(posted, "/")
	req := actions.AddRequest{
		Kind: k.Kind, Namespace: ns, Title: r.PostFormValue("title"), ProviderID: id,
		RootFolderRef: root, QualityProfileRef: r.PostFormValue("qualityProfile"),
		Monitored: checked(r, "monitored"), Monitor: r.PostFormValue("monitor"),
		SearchOnAdd: checked(r, "searchOnAdd"), MinimumAvailability: r.PostFormValue("minimumAvailability"),
		SeriesType: r.PostFormValue("seriesType"), SeasonFolder: checked(r, "seasonFolder"),
		SearchCutoffUnmet: checked(r, "searchCutoffUnmet"), MonitorNewItems: r.PostFormValue("monitorNewItems"),
	}
	name, _, err := s.opts.Actions.AddItem(r.Context(), req)
	if err != nil {
		code, status := actionErrorCode(err)
		logging.FromContext(r.Context()).Error("add rejected", "kind", k.Kind, "error", err, "code", code)
		s.renderAddPage(w, r, k, status, code, err.Error())
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
	default:
		return false
	}
	return s.opts.Reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, obj) == nil
}
