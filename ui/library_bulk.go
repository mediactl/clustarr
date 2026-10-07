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
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
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
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	action := r.FormValue("action")
	if !slices.Contains([]string{"monitor", "unmonitor", "search", "refresh", "delete"}, action) {
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
		_, err := s.opts.Actions.SetMonitored(ctx, ns, kind, name, action == "monitor")
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
	default: // delete; RequestDelete refuses a kind deleted with its parent
		// An exclusion only where the kind has one, as the item's dialog
		// offers it only there.
		_, excludable := catalogv1.ExclusionKindFor(kind)
		_, err := s.opts.Actions.RequestDelete(ctx, ns, kind, name,
			r.FormValue("files") == "true", excludable && r.FormValue("exclude") == "true")
		return err
	}
}

// splitItemRef splits a bulk "item" field, namespace/kind/name.
func splitItemRef(ref string) (namespace, kind, name string, ok bool) {
	parts := strings.Split(ref, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}
