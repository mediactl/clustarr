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
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/views"
)

// The Hubs page's handlers
// (docs/superpowers/specs/2026-11-16-plex-hub-layouts-design.md): the page,
// a library's section, and the two writes through ui/actions -- one hub
// edit, and the reset. The Collections page's three switches post the same
// edit.

const hubsPath = "/library/hubs"

func (s *Server) plexHubLayouts(ctx context.Context) ([]catalogv1.PlexHubLayout, error) {
	if s.opts.Reader == nil {
		return nil, nil
	}
	var list catalogv1.PlexHubLayoutList
	if err := s.opts.Reader.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list hub layouts: %w", err)
	}
	return list.Items, nil
}

// libraryKey tells one namespace's library from another's of the title.
type libraryKey struct{ namespace, title string }

// hubLayouts are the layouts by library. Two layouts of one library are
// one too many; the first by name stands.
func hubLayouts(layouts []catalogv1.PlexHubLayout) map[libraryKey]*catalogv1.PlexHubLayout {
	out := map[libraryKey]*catalogv1.PlexHubLayout{}
	for i := range layouts {
		l := &layouts[i]
		k := libraryKey{l.Namespace, l.Spec.Library}
		if have, ok := out[k]; !ok || l.Name < have.Name {
			out[k] = l
		}
	}
	return out
}

// hubLibrary is a library's section: l is its layout, or nil for a library
// none holds yet, which namespace, name and title then describe.
func hubLibrary(l *catalogv1.PlexHubLayout, namespace, name, title string, collections []catalogv1.PlexCollection) views.HubLibrary {
	v := views.HubLibrary{Namespace: namespace, Name: name, Title: title}
	if l == nil {
		return v
	}
	v.Namespace, v.Name, v.Title, v.Exists = l.Namespace, l.Name, l.Spec.Library, true
	ready := meta.FindStatusCondition(l.Status.Conditions, catalogv1.PlexHubLayoutConditionReady)
	v.Reported = ready != nil || len(l.Status.Hubs) > 0 || l.Status.ObservedGeneration > 0
	if ready != nil && ready.Status != metav1.ConditionTrue {
		v.NotReady = ready.Message
		if v.NotReady == "" {
			v.NotReady = ready.Reason
		}
	}
	if at := l.Annotations[catalogv1.AnnotationPlexHubsReset]; at != "" {
		v.ResetPending = at != l.Status.ResetHandled
	}
	titles := map[string]string{}
	for _, pc := range collections {
		if pc.Namespace == l.Namespace {
			titles[pc.Name] = collectionTitle(pc.Spec)
		}
	}
	shown := map[string]bool{}
	for _, r := range actions.HubRows(l) {
		row := views.HubRow{
			Key: r.Ref.Key(), Title: r.Title, Collection: r.Kind == catalogv1.PlexHubCollection,
			Listed: r.Listed, InPlex: r.InPlex,
			LibraryRecommended: r.LibraryRecommended, Home: r.Home, FriendsHome: r.FriendsHome,
		}
		if r.Ref.Collection != "" {
			shown[r.Ref.Collection] = true
			// A collection Plex shows no hub for yet has no title in the
			// status: the resource's own.
			if t := titles[r.Ref.Collection]; t != "" && !r.InPlex {
				row.Title = t
			}
		}
		v.Rows = append(v.Rows, row)
	}
	for _, pc := range collections {
		if pc.Namespace == l.Namespace && pc.Spec.Library == l.Spec.Library && !shown[pc.Name] {
			v.Addable = append(v.Addable, views.Choice{Key: pc.Name, Label: collectionTitle(pc.Spec)})
		}
	}
	slices.SortFunc(v.Addable, func(a, b views.Choice) int {
		return strings.Compare(strings.ToLower(a.Label), strings.ToLower(b.Label))
	})
	return v
}

func (s *Server) hubsPage(ctx context.Context) views.HubsPage {
	layouts, err := s.plexHubLayouts(ctx)
	if err != nil {
		logging.FromContext(ctx).Error("list hub layouts", "err", err)
	}
	collections, err := s.plexCollections(ctx)
	if err != nil {
		logging.FromContext(ctx).Error("list collections", "err", err)
	}
	byLibrary := hubLayouts(layouts)
	var p views.HubsPage
	for _, l := range byLibrary {
		p.Libraries = append(p.Libraries, hubLibrary(l, "", "", "", collections))
	}
	// The libraries the Collections page knows, which no layout holds yet.
	seen := map[libraryKey]bool{}
	for _, pc := range collections {
		k := libraryKey{pc.Namespace, pc.Spec.Library}
		if byLibrary[k] != nil || seen[k] {
			continue
		}
		seen[k] = true
		p.Libraries = append(p.Libraries, hubLibrary(nil, k.namespace, actions.PlexHubLayoutName(k.title), k.title, nil))
	}
	slices.SortFunc(p.Libraries, func(a, b views.HubLibrary) int {
		if c := strings.Compare(a.Title, b.Title); c != 0 {
			return c
		}
		return strings.Compare(a.Namespace, b.Namespace)
	})
	return p
}

// collectionHubFlags are a collection's three switches on the Collections
// page: its entry's in the library's layout when one lists it, else what
// Plex reports for its hub, else its own spec.visibility, which holds
// until a layout lists it.
func collectionHubFlags(pc catalogv1.PlexCollection, l *catalogv1.PlexHubLayout) views.HubFlags {
	f := views.HubFlags{
		Namespace: pc.Namespace, Layout: actions.PlexHubLayoutName(pc.Spec.Library),
		Library: pc.Spec.Library, Collection: pc.Name,
	}
	if v := pc.Spec.Visibility; v != nil {
		f.LibraryRecommended, f.Home, f.FriendsHome = v.LibraryRecommended, v.Home, v.FriendsHome
	}
	if l == nil {
		return f
	}
	f.Layout = l.Name
	for _, r := range actions.HubRows(l) {
		if r.Ref.Collection == pc.Name {
			f.LibraryRecommended, f.Home, f.FriendsHome = r.LibraryRecommended, r.Home, r.FriendsHome
			break
		}
	}
	return f
}

func (s *Server) handleHubs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.Hubs(s.hubsPage(r.Context())).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render the Hubs page", "err", err)
	}
}

// handleHubSection answers one library's section, which asks for itself
// while it waits on cluster-plex.
func (s *Server) handleHubSection(w http.ResponseWriter, r *http.Request) {
	if s.opts.Reader == nil {
		http.NotFound(w, r)
		return
	}
	var l catalogv1.PlexHubLayout
	key := client.ObjectKey{Namespace: r.PathValue("namespace"), Name: r.PathValue("name")}
	if err := s.opts.Reader.Get(r.Context(), key, &l); err != nil {
		if apierrors.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	collections, _ := s.plexCollections(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.HubLibrarySection(hubLibrary(&l, "", "", "", collections)).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render a hub library", "err", err)
	}
}

// parseHubEdit reads a hub edit's form: op (ensure, flag, move, add,
// remove), hub (an actions.HubRef key; add takes collection, a
// PlexCollection's name, instead), flag and on for a flag, to for a move.
func parseHubEdit(v url.Values) (actions.HubEdit, error) {
	e := actions.HubEdit{Op: actions.HubOp(v.Get("op")), Flag: v.Get("flag")}
	switch e.Op {
	case actions.HubEnsure:
		return e, nil
	case actions.HubAdd:
		if v.Get("hub") == "" {
			if v.Get("collection") == "" {
				return e, fmt.Errorf("%w: choose a collection to add", actions.ErrInvalid)
			}
			e.Ref = actions.HubRef{Collection: v.Get("collection")}
			return e, nil
		}
	case actions.HubSetFlag:
		on, err := strconv.ParseBool(v.Get("on"))
		if err != nil {
			return e, fmt.Errorf("%w: on %q", actions.ErrInvalid, v.Get("on"))
		}
		e.On = on
	case actions.HubMove:
		to, err := strconv.Atoi(v.Get("to"))
		if err != nil {
			return e, fmt.Errorf("%w: position %q", actions.ErrInvalid, v.Get("to"))
		}
		e.To = to
	case actions.HubRemove:
	default:
		return e, fmt.Errorf("%w: hub edit %q", actions.ErrInvalid, v.Get("op"))
	}
	ref, err := actions.ParseHubRef(v.Get("hub"))
	if err != nil {
		return e, err
	}
	e.Ref = ref
	return e, nil
}

func hubEditDone(op actions.HubOp) string {
	switch op {
	case actions.HubEnsure:
		return "Hubs managed"
	case actions.HubMove:
		return "Hub moved"
	case actions.HubAdd:
		return "Collection added as a hub"
	case actions.HubRemove:
		return "Hub no longer listed"
	}
	return "Hub updated"
}

// handleHubEdit writes one hub edit. An htmx control is answered into
// #action-status with the toast and, out of band, what it changed, drawn
// from the layout as written: the library's rows, or for the Collections
// page (view=collection) the collection's switches. A form is redirected
// to its return.
func (s *Server) handleHubEdit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	e, err := parseHubEdit(r.PostForm)
	var l *catalogv1.PlexHubLayout
	if err == nil {
		l, err = s.opts.Actions.EditPlexHubLayout(r.Context(), ns, name, r.PostForm.Get("library"), e)
		switch {
		case apierrors.IsAlreadyExists(err):
			err = errors.New("the library's layout was created meanwhile: try again")
		case apierrors.IsConflict(err), err != nil && strings.Contains(err.Error(), "/metadata/generation"):
			err = errors.New("the layout was edited meanwhile: try again")
		}
	}
	done := hubEditDone(e.Op)
	if err != nil || !cardAction(r) {
		s.finishActionWith(w, r, err, done)
		return
	}
	collections, _ := s.plexCollections(r.Context())
	var view templ.Component = views.HubEditAnswer(done, hubLibrary(l, "", "", "", collections))
	if r.PostForm.Get("view") == "collection" && e.Ref.Collection != "" {
		pc := catalogv1.PlexCollection{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: e.Ref.Collection}}
		pc.Spec.Library = l.Spec.Library
		view = views.CollectionFlagsAnswer(done, collectionHubFlags(pc, l))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := view.Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render a hub edit's answer", "err", err)
	}
}

// handleHubReset asks for Plex's "Reset Customization" of the library.
func (s *Server) handleHubReset(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	err := s.opts.Actions.ResetPlexHubLayout(r.Context(), r.PathValue("namespace"), r.PathValue("name"), r.PostForm.Get("library"), s.now())
	s.finishActionWith(w, r, err, "Reset requested")
}
