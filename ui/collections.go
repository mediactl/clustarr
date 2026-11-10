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
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/plexfilter"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/views"
)

// The Collections page's handlers
// (docs/superpowers/specs/2026-11-10-plex-collections-design.md): the list,
// the editor (a page; its buttons answer with the form redrawn), and the
// three writes through ui/actions. The spec is checked here, before a
// write (pkg/plexfilter.Validate); ui/actions may not import it.

const collectionsPath = "/library/collections"

// languageSuggestions are ISO 639-1 codes a language field offers.
var languageSuggestions = []string{"ar", "de", "en", "es", "fr", "hi", "it", "ja", "ko", "nl", "pl", "pt", "ru", "sv", "zh"}

func (s *Server) plexCollections(ctx context.Context) ([]catalogv1.PlexCollection, error) {
	if s.opts.Reader == nil {
		return nil, nil
	}
	var list catalogv1.PlexCollectionList
	if err := s.opts.Reader.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list collections: %w", err)
	}
	return list.Items, nil
}

func (s *Server) collectionsPage(ctx context.Context) views.CollectionsPage {
	items, err := s.plexCollections(ctx)
	if err != nil {
		logging.FromContext(ctx).Error("list collections", "error", err)
	}
	byLibrary := map[string][]views.CollectionRow{}
	for _, pc := range items {
		row := views.CollectionRow{
			Namespace: pc.Namespace, Name: pc.Name, Title: pc.Spec.Title,
			Reading: plexfilter.Describe(pc.Spec), Items: pc.Status.ItemCount,
		}
		if c := meta.FindStatusCondition(pc.Status.Conditions, catalogv1.PlexCollectionConditionSynced); c != nil {
			row.Reason, row.Message, row.Synced = c.Reason, c.Message, c.Status == metav1.ConditionTrue
		}
		byLibrary[pc.Spec.Library] = append(byLibrary[pc.Spec.Library], row)
	}
	var p views.CollectionsPage
	for lib, rows := range byLibrary {
		slices.SortFunc(rows, func(a, b views.CollectionRow) int {
			return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
		})
		p.Libraries = append(p.Libraries, views.CollectionLibrary{Title: lib, Rows: rows})
	}
	slices.SortFunc(p.Libraries, func(a, b views.CollectionLibrary) int { return strings.Compare(a.Title, b.Title) })
	return p
}

func (s *Server) handleCollections(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.Collections(s.collectionsPage(r.Context())).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render the Collections page", "error", err)
	}
}

func (s *Server) handleCollectionsList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.CollectionsList(s.collectionsPage(r.Context())).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render the Collections list", "error", err)
	}
}

// editorModel is the editor for spec: the libraries the existing
// collections name and the value suggestions for its item type.
func (s *Server) editorModel(ctx context.Context, namespace, name, rv string, spec catalogv1.PlexCollectionSpec, why string) views.CollectionEditor {
	e := views.CollectionEditor{Namespace: namespace, Name: name, ResourceVersion: rv, Spec: spec, Error: why}
	libs := map[string]bool{}
	if spec.Library != "" {
		libs[spec.Library] = true
	}
	items, _ := s.plexCollections(ctx)
	for _, pc := range items {
		libs[pc.Spec.Library] = true
	}
	for l := range libs {
		e.Libraries = append(e.Libraries, l)
	}
	slices.Sort(e.Libraries)
	e.Suggestions = s.valueSuggestions(ctx, spec.ItemType)
	return e
}

// valueSuggestions are the values clustarr's own metadata offers for a
// field -- where Plex got them, clustarr being its metadata provider --
// read from the cached Movies or Series, never the Episodes.
func (s *Server) valueSuggestions(ctx context.Context, t catalogv1.PlexItemType) map[string][]string {
	sets := map[string]map[string]bool{}
	add := func(field string, vals ...string) {
		for _, v := range vals {
			if v = strings.TrimSpace(v); v == "" {
				continue
			}
			if sets[field] == nil {
				sets[field] = map[string]bool{}
			}
			sets[field][v] = true
		}
	}
	if s.opts.Reader != nil {
		if t == catalogv1.PlexItemMovie {
			var movies catalogv1.MovieList
			if err := s.opts.Reader.List(ctx, &movies); err == nil {
				for _, m := range movies.Items {
					if md := m.Status.Metadata; md != nil {
						add("genre", md.Genres...)
						add("studio", md.Studios...)
					}
				}
			}
		} else {
			var series catalogv1.SeriesList
			if err := s.opts.Reader.List(ctx, &series); err == nil {
				for _, sr := range series.Items {
					if md := sr.Status.Metadata; md != nil {
						add("show.genre", md.Genres...)
						add("show.network", md.Network)
						add("show.network", md.Networks...)
						add("show.studio", md.Studios...)
					}
				}
			}
		}
	}
	for _, f := range plexfilter.Fields(t) {
		if f.Type == plexfilter.TypeAudioLanguage || f.Type == plexfilter.TypeSubtitleLanguage {
			add(f.Key, languageSuggestions...)
		}
	}
	out := make(map[string][]string, len(sets))
	for field, set := range sets {
		vals := make([]string, 0, len(set))
		for v := range set {
			vals = append(vals, v)
		}
		slices.Sort(vals)
		out[field] = vals
	}
	return out
}

func (s *Server) renderEditorPage(w http.ResponseWriter, r *http.Request, status int, e views.CollectionEditor) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := views.CollectionEditorPage(e).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render the collection editor", "error", err)
	}
}

func (s *Server) handleCollectionNew(w http.ResponseWriter, r *http.Request) {
	t := catalogv1.PlexItemType(r.URL.Query().Get("itemType"))
	if _, ok := map[catalogv1.PlexItemType]bool{catalogv1.PlexItemMovie: true, catalogv1.PlexItemShow: true, catalogv1.PlexItemSeason: true, catalogv1.PlexItemEpisode: true}[t]; !ok {
		t = catalogv1.PlexItemMovie
	}
	c := blankCondition(t)
	spec := catalogv1.PlexCollectionSpec{ItemType: t, Filter: &catalogv1.PlexFilter{
		Match: catalogv1.PlexMatchAll, Rules: []catalogv1.PlexFilterRule{{Field: c.Field, Operator: c.Operator}},
	}}
	s.renderEditorPage(w, r, http.StatusOK, s.editorModel(r.Context(), s.opts.Namespace, "", "", spec, ""))
}

func (s *Server) handleCollectionEdit(w http.ResponseWriter, r *http.Request) {
	if s.opts.Reader == nil {
		http.NotFound(w, r)
		return
	}
	var pc catalogv1.PlexCollection
	key := client.ObjectKey{Namespace: r.PathValue("namespace"), Name: r.PathValue("name")}
	if err := s.opts.Reader.Get(r.Context(), key, &pc); err != nil {
		if apierrors.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.renderEditorPage(w, r, http.StatusOK, s.editorModel(r.Context(), pc.Namespace, pc.Name, pc.ResourceVersion, pc.Spec, ""))
}

// handleCollectionEditor answers an editor button: the form as posted,
// changed by the op, redrawn. Nothing is written.
func (s *Server) handleCollectionEditor(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	spec, perr := parseCollectionForm(r.PostForm)
	op := r.URL.Query().Get("op")
	if err := editorOp(&spec, op, r.URL.Query().Get("at")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if op == "refresh" && spec.Filter != nil && len(spec.Filter.Rules) == 0 {
		c := blankCondition(spec.ItemType)
		spec.Filter.Rules = []catalogv1.PlexFilterRule{{Field: c.Field, Operator: c.Operator}}
	}
	why := ""
	if perr != nil {
		why = perr.Error()
	}
	e := s.editorModel(r.Context(), r.PostForm.Get("namespace"), r.PostForm.Get("name"), r.PostForm.Get("resourceVersion"), spec, why)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.CollectionEditorForm(e).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render the collection editor", "error", err)
	}
}

// savedSpec parses and checks the posted spec; the error is what the
// editor shows.
func savedSpec(r *http.Request) (catalogv1.PlexCollectionSpec, error) {
	if err := r.ParseForm(); err != nil {
		return catalogv1.PlexCollectionSpec{}, err
	}
	spec, err := parseCollectionForm(r.PostForm)
	if err != nil {
		return spec, err
	}
	return spec, plexfilter.Validate(spec)
}

func (s *Server) handleCollectionCreate(w http.ResponseWriter, r *http.Request) {
	spec, err := savedSpec(r)
	ns := s.opts.Namespace
	name := actions.PlexCollectionName(spec.Title)
	if err == nil && name == "" {
		err = errors.New("title: needs a letter or a digit")
	}
	if err == nil {
		_, err = s.opts.Actions.CreatePlexCollection(r.Context(), ns, name, spec)
	}
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			err = fmt.Errorf("a collection named %q exists already: choose another title", name)
		}
		s.renderEditorPage(w, r, http.StatusBadRequest, s.editorModel(r.Context(), ns, "", "", spec, err.Error()))
		return
	}
	http.Redirect(w, r, collectionsPath, http.StatusSeeOther)
}

func (s *Server) handleCollectionUpdate(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	spec, err := savedSpec(r)
	rv := r.PostForm.Get("resourceVersion")
	if err == nil {
		err = s.opts.Actions.UpdatePlexCollection(r.Context(), ns, name, rv, spec)
		if apierrors.IsConflict(err) || apierrors.IsInvalid(err) && strings.Contains(err.Error(), "resourceVersion") {
			err = errors.New("the collection changed since this editor opened it: reload it and edit again")
		}
	}
	if err != nil {
		s.renderEditorPage(w, r, http.StatusBadRequest, s.editorModel(r.Context(), ns, name, rv, spec, err.Error()))
		return
	}
	http.Redirect(w, r, collectionsPath, http.StatusSeeOther)
}

func (s *Server) handleCollectionDelete(w http.ResponseWriter, r *http.Request) {
	err := s.opts.Actions.DeletePlexCollection(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	if err == nil {
		w.Header().Set("HX-Trigger", "collections-changed")
	}
	s.finishActionWith(w, r, err, "Collection deleted")
}
