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
	"sync"
	"time"

	"github.com/a-h/templ"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/views"
)

func init() {
	addRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("POST /library/{namespace}/{kind}/{name}/rows/bulk", s.handleRowsBulk)
	})
}

// maxBulkSearches caps the single searches one bulk request starts: each
// is a live search behind the namespace's pace. A whole season, or a whole
// comic, is one search and does not count.
const maxBulkSearches = 25

// rowChild is the child kind of a container whose page lists its children
// in a selectable table.
var rowChild = map[commonv1.MediaKind]commonv1.MediaKind{
	commonv1.MediaKindSeries: commonv1.MediaKindEpisode,
	commonv1.MediaKindArtist: commonv1.MediaKindAlbum,
	commonv1.MediaKindAuthor: commonv1.MediaKindBook,
	commonv1.MediaKindComic:  commonv1.MediaKindIssue,
}

// bulkRow is one selected child: the object as read, replaced by the
// object a write returned.
type bulkRow struct {
	name string
	obj  client.Object
}

// handleRowsBulk is the bulk edit of a container's child rows (owner,
// 2026-11-17: inner tables are selectable and editable): POST
// /library/{namespace}/{kind}/{name}/rows/bulk, {kind}/{name} the
// container -- a series (its episodes), an artist (albums), an author
// (books) or a comic (issues) -- with an "action" of monitor, unmonitor or
// search and the selected children as repeated "item" fields, each
// namespace/kind/name. Every item must be a child of the container, checked
// before any write, so the form reaches only what the page showed. Each
// write is the row's own action (setMonitored, Actions.SearchNow), never a
// write of its own; a search of every episode of a season is one season
// search, of every issue of a comic one volume search, and at most
// maxBulkSearches single searches start in one request. Every item is
// attempted and the failures are reported together.
//
// The answer is a toast, then for monitor and unmonitor each written row
// out of band, drawn from the object its write returned (never the cache,
// which lags the write), and the rows-bulk-done event when nothing failed,
// at which the page's selection clears; a failure keeps it.
func (s *Server) handleRowsBulk(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ns, container := r.PathValue("namespace"), r.PathValue("name")
	containerKind := commonv1.MediaKind(r.PathValue("kind"))
	childKind, ok := rowChild[containerKind]
	if !ok {
		notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		badRequest(w, r, "invalid form")
		return
	}
	action := r.FormValue("action")
	refs := r.Form["item"]
	switch {
	case action != "monitor" && action != "unmonitor" && action != "search":
		s.finishActionWith(w, r, fmt.Errorf("%w: unknown bulk action %q", actions.ErrInvalid, action), "")
		return
	case len(refs) == 0:
		s.finishActionWith(w, r, fmt.Errorf("%w: nothing is selected", actions.ErrInvalid), "")
		return
	case len(refs) > maxBulkItems:
		s.finishActionWith(w, r, fmt.Errorf("%w: %d rows selected, at most %d at once", actions.ErrInvalid, len(refs), maxBulkItems), "")
		return
	case s.opts.Reader == nil:
		s.finishActionWith(w, r, actions.ErrNoWriter, "")
		return
	}

	rows := make([]bulkRow, 0, len(refs))
	seen := map[string]bool{}
	for _, ref := range refs {
		rns, kind, name, ok := splitItemRef(ref)
		if !ok || rns != ns || commonv1.MediaKind(kind) != childKind {
			s.finishActionWith(w, r, fmt.Errorf("%w: %q is not %s %s of this %s", actions.ErrInvalid, ref, article(childKind), childKind, containerKind), "")
			return
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		obj, parent, err := s.childOf(ctx, ns, childKind, name)
		if err != nil || parent != container {
			s.finishActionWith(w, r, fmt.Errorf("%w: %q is not %s %s of this %s", actions.ErrInvalid, ref, article(childKind), childKind, containerKind), "")
			return
		}
		rows = append(rows, bulkRow{name: name, obj: obj})
	}

	var (
		errs []error
		done string
	)
	if action == "search" {
		done, errs = s.bulkSearch(ctx, ns, containerKind, container, childKind, rows)
	} else {
		errs = s.bulkMonitor(ctx, ns, childKind, rows, action == "monitor")
		done = fmt.Sprintf("%s %s", countNoun(len(rows)-len(errs), childKind), map[bool]string{true: "monitored", false: "unmonitored"}[action == "monitor"])
	}

	if !cardAction(r) {
		// A plain post, or htmx aimed elsewhere: the shared tail.
		s.finishAction(w, r, errors.Join(errs...))
		return
	}
	if len(errs) == 0 {
		w.Header().Set("HX-Trigger-After-Settle", "rows-bulk-done")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var toast templ.Component
	if len(errs) > 0 {
		code, _ := actionErrorCode(errs[0])
		logging.FromContext(ctx).Error("ui bulk action failed", "err", errors.Join(errs...), "failed", len(errs), "of", len(rows))
		toast = views.ActionErrorToast(code, fmt.Sprintf("%d of %d failed: %v", len(errs), len(rows), errs[0]))
	} else {
		toast = views.ActionStatus(done)
	}
	if err := toast.Render(ctx, w); err != nil {
		logging.FromContext(ctx).Error("render bulk status", "err", err)
		return
	}
	if action == "search" {
		return
	}
	now := time.Now()
	for _, row := range rows {
		if view := s.rowView(ctx, childKind, row.obj, now); view != nil {
			if err := views.OOBRow(view).Render(ctx, w); err != nil {
				logging.FromContext(ctx).Error("render bulk row", "err", err)
				return
			}
		}
	}
}

// childOf reads the child ns/name and the name of the container it
// belongs to.
func (s *Server) childOf(ctx context.Context, ns string, kind commonv1.MediaKind, name string) (client.Object, string, error) {
	key := types.NamespacedName{Namespace: ns, Name: name}
	switch kind {
	case commonv1.MediaKindEpisode:
		var o catalogv1.Episode
		err := s.opts.Reader.Get(ctx, key, &o)
		return &o, o.Spec.SeriesRef, err
	case commonv1.MediaKindAlbum:
		var o catalogv1.Album
		err := s.opts.Reader.Get(ctx, key, &o)
		return &o, o.Spec.ArtistRef, err
	case commonv1.MediaKindBook:
		var o catalogv1.Book
		err := s.opts.Reader.Get(ctx, key, &o)
		parent := ""
		if o.Spec.AuthorRef != nil {
			parent = *o.Spec.AuthorRef
		}
		return &o, parent, err
	case commonv1.MediaKindIssue:
		var o catalogv1.Issue
		err := s.opts.Reader.Get(ctx, key, &o)
		return &o, o.Spec.ComicRef, err
	}
	return nil, "", fmt.Errorf("%w: %s rows are not selectable", actions.ErrInvalid, kind)
}

// bulkMonitor sets every row's monitored flag, eight writes in flight,
// keeping the object each write returned in the row's place.
func (s *Server) bulkMonitor(ctx context.Context, ns string, kind commonv1.MediaKind, rows []bulkRow, monitored bool) []error {
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
		slot = make(chan struct{}, 8)
	)
	for i := range rows {
		wg.Add(1)
		slot <- struct{}{}
		go func(row *bulkRow) {
			defer wg.Done()
			defer func() { <-slot }()
			patched, err := s.setMonitored(ctx, ns, kind, row.name, monitored)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s %s: %w", kind, row.name, err))
				return
			}
			if patched != nil {
				row.obj = patched
			}
		}(&rows[i])
	}
	wg.Wait()
	return errs
}

// bulkSearch searches the selected rows: a season whose every episode is
// selected as one season search, a comic whose every issue is selected as
// one volume search, the rest one search each, refused above
// maxBulkSearches.
func (s *Server) bulkSearch(ctx context.Context, ns string, containerKind commonv1.MediaKind, container string, kind commonv1.MediaKind, rows []bulkRow) (string, []error) {
	singles := make([]string, 0, len(rows))
	var seasons []int32
	volume := false
	switch containerKind {
	case commonv1.MediaKindSeries:
		all, err := s.listEpisodes(ctx, ns, container)
		if err != nil {
			return "", []error{err}
		}
		total := map[int32]int{}
		for i := range all {
			total[all[i].Spec.SeasonNumber]++
		}
		picked := map[int32][]string{}
		for _, row := range rows {
			ep := row.obj.(*catalogv1.Episode)
			picked[ep.Spec.SeasonNumber] = append(picked[ep.Spec.SeasonNumber], row.name)
		}
		for n, names := range picked {
			if len(names) > 1 && len(names) == total[n] {
				seasons = append(seasons, n)
			} else {
				singles = append(singles, names...)
			}
		}
	case commonv1.MediaKindComic:
		all, err := s.listIssues(ctx, ns, container)
		if err != nil {
			return "", []error{err}
		}
		if len(rows) > 1 && len(rows) == len(all) {
			volume = true
			break
		}
		fallthrough
	default:
		for _, row := range rows {
			singles = append(singles, row.name)
		}
	}
	if len(singles) > maxBulkSearches {
		whole := "fewer rows"
		if containerKind == commonv1.MediaKindSeries {
			whole = "whole seasons"
		}
		return "", []error{fmt.Errorf("%w: %d single searches at once; select %s, or at most %d", actions.ErrInvalid, len(singles), whole, maxBulkSearches)}
	}
	var errs []error
	for _, n := range seasons {
		if _, err := s.opts.Actions.SearchSeason(ctx, ns, container, n); err != nil {
			errs = append(errs, fmt.Errorf("season %d: %w", n, err))
		}
	}
	if volume {
		if _, err := s.opts.Actions.SearchVolume(ctx, ns, container); err != nil {
			errs = append(errs, fmt.Errorf("comic %s: %w", container, err))
		}
	}
	for _, name := range singles {
		if _, err := s.opts.Actions.SearchNow(ctx, ns, kind, name); err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", kind, name, err))
		}
	}
	return "Search queued for " + countNoun(len(rows), kind), errs
}

// rowView is a child's row as its container's page draws it.
func (s *Server) rowView(ctx context.Context, kind commonv1.MediaKind, obj client.Object, now time.Time) templ.Component {
	switch o := obj.(type) {
	case *catalogv1.Episode:
		return views.EpisodeRowView(episodeRow(o, s.episodeFile(ctx, o), now))
	case *catalogv1.Album:
		return views.AlbumRowView(albumTableRow(o, now))
	case *catalogv1.Book:
		return views.BookRowView(bookRow(o, now))
	case *catalogv1.Issue:
		return views.IssueRowView(issueRow(o, now))
	}
	return nil
}

// countNoun is "1 episode", "12 episodes".
func countNoun(n int, kind commonv1.MediaKind) string {
	if n == 1 {
		return "1 " + string(kind)
	}
	return fmt.Sprintf("%d %ss", n, kind)
}

// article is the indefinite article of a child kind's name.
func article(kind commonv1.MediaKind) string {
	switch kind {
	case commonv1.MediaKindEpisode, commonv1.MediaKindAlbum, commonv1.MediaKindIssue:
		return "an"
	}
	return "a"
}
