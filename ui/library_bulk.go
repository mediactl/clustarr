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

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// maxBulkItems caps one mass edit: a page's window is at most a few hundred
// cards, and every item is one apiserver write.
const maxBulkItems = 500

// handleLibraryBulk is the library's mass editor (2026-10-06, after
// Sonarr's): POST /library/{tab}/bulk with an "action" -- monitor,
// unmonitor, search, refresh or delete -- and the selected items as
// repeated "item" fields, each namespace/kind/name. Every item must be on
// the tab in the library projection, so the form can only reach what the
// page showed; each is then the item page's own action (handleSetMonitored,
// handleSearchNow, handleRefreshMetadata with scan, handleRequestDelete
// with "files" and "exclude"), through Options.Actions, never a write of
// its own. Every item is attempted; the failures are reported together
// through finishAction, which otherwise redirects to "return".
func (s *Server) handleLibraryBulk(w http.ResponseWriter, r *http.Request) {
	tab, ok := projection.ParseTab(r.PathValue("tab"))
	if !ok {
		notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	action := r.FormValue("action")
	if !slices.Contains([]string{"monitor", "unmonitor", "search", "refresh", "delete", "move"}, action) {
		s.finishAction(w, r, fmt.Errorf("%w: unknown bulk action %q", actions.ErrInvalid, action))
		return
	}
	refs := r.Form["item"]
	switch {
	case len(refs) == 0:
		s.finishAction(w, r, fmt.Errorf("%w: nothing is selected", actions.ErrInvalid))
		return
	case len(refs) > maxBulkItems:
		s.finishAction(w, r, fmt.Errorf("%w: %d items selected, at most %d at once", actions.ErrInvalid, len(refs), maxBulkItems))
		return
	}

	library := projection.ForTab(s.opts.Library(r.Context()), tab)
	items := make([]projection.LibraryItem, 0, len(refs))
	for _, ref := range refs {
		ns, kind, name, ok := splitItemRef(ref)
		item, found := findLibraryItem(library, ns, kind, name)
		if !ok || !found {
			s.finishAction(w, r, fmt.Errorf("%w: %q is not on the %s tab", actions.ErrInvalid, ref, tab))
			return
		}
		items = append(items, item)
	}

	var errs []error
	for _, item := range items {
		if err := s.bulkOne(r, action, item); err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", item.Kind, item.Ref, err))
		}
	}
	s.finishAction(w, r, errors.Join(errs...))
}

// bulkOne applies the action to one item, as its own page's action does.
func (s *Server) bulkOne(r *http.Request, action string, item projection.LibraryItem) error {
	ctx, ns, kind, name := r.Context(), item.Ref.Namespace, item.Kind, item.Ref.Name
	switch action {
	case "monitor", "unmonitor":
		_, err := s.setMonitored(ctx, ns, kind, name, action == "monitor")
		return err
	case "search":
		_, err := s.opts.Actions.SearchNow(ctx, ns, kind, name)
		return err
	case "refresh":
		if _, err := s.opts.Actions.RefreshMetadata(ctx, ns, kind, name); err != nil {
			return err
		}
		if root, sub, ok := s.itemFolder(ctx, ns, kind, name); ok {
			_, err := s.opts.Actions.RescanPath(ctx, ns, root, sub)
			return err
		}
		return nil
	case "move":
		return s.moveSeries(ctx, item, r.FormValue("rootFolder"))
	default: // delete; RequestDelete refuses a kind deleted with its parent
		// An exclusion only where the kind has one, as the item's dialog
		// offers it only there.
		_, excludable := catalogv1.ExclusionKindFor(kind)
		_, err := s.opts.Actions.RequestDelete(ctx, ns, kind, name,
			r.FormValue("files") == "true", excludable && r.FormValue("exclude") == "true")
		return err
	}
}

// moveSeries stores a Series under the root folder target names
// ("namespace/name"), which moves it between the TV and Anime tabs and the
// two Plex libraries: the root folder decides the library (anime library
// design, 2026-11-25, §1, §5). One spec patch -- the root folder, the
// series type the folder makes it (anime under an anime root; under any
// other, an anime becomes the folder's default type and a daily or standard
// series keeps its own) and the folder's default quality profile, the
// series' own where the folder names none -- through the item's Edit. Its
// files follow by themselves, one at a time (the rename actuator's
// relocation). A series already there is left.
func (s *Server) moveSeries(ctx context.Context, item projection.LibraryItem, target string) error {
	if item.Kind != commonv1.MediaKindSeries {
		return fmt.Errorf("%w: only a series is moved between root folders", actions.ErrInvalid)
	}
	ns, name, ok := strings.Cut(target, "/")
	var rf *catalogv1.RootFolder
	if ok {
		rf = s.rootFolderNamed(ctx, ns, name)
	}
	if rf == nil || ns != item.Ref.Namespace || rf.Spec.Kind != catalogv1.RootFolderKindSeries {
		return fmt.Errorf("%w: %q is not a series root folder beside it", actions.ErrInvalid, target)
	}
	if rf.Name == item.RootFolder {
		return nil
	}
	req := actions.EditRequest{RootFolderRef: rf.Name, QualityProfileRef: item.QualityProfileRef}
	if p := rf.Spec.Defaults.QualityProfileRef; p != "" {
		req.QualityProfileRef = p
	}
	anime := string(catalogv1.SeriesTypeAnime)
	switch {
	case rf.Spec.AnimeRoot():
		req.SeriesType = anime
	case item.SeriesType == anime:
		req.SeriesType = string(rf.Spec.Defaults.SeriesType)
		if req.SeriesType == "" || req.SeriesType == anime {
			req.SeriesType = string(catalogv1.SeriesTypeStandard)
		}
	}
	_, err := s.opts.Actions.EditItem(ctx, item.Ref.Namespace, item.Kind, item.Ref.Name, req)
	return err
}

// moveTargets are the root folders the tab's Move offers: every series
// root folder that is not the tab's own, so TV offers the anime roots and
// Anime the others. No other tab moves anything.
func moveTargets(folders []catalogv1.RootFolder, tab projection.Tab) []views.AddOption {
	if tab != projection.TabTV && tab != projection.TabAnime {
		return nil
	}
	var out []views.AddOption
	for i := range folders {
		rf := &folders[i]
		if rf.Spec.Kind != catalogv1.RootFolderKindSeries || projection.RootFolderOnTab(rf, tab) {
			continue
		}
		to := projection.TabTV
		if rf.Spec.AnimeRoot() {
			to = projection.TabAnime
		}
		out = append(out, views.AddOption{Value: rf.Namespace + "/" + rf.Name, Label: views.TabLabel(to) + " · " + rf.Name + " (" + rf.Spec.Path + ")"})
	}
	return out
}

// splitItemRef splits a bulk "item" field, namespace/kind/name.
func splitItemRef(ref string) (namespace, kind, name string, ok bool) {
	parts := strings.Split(ref, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}
