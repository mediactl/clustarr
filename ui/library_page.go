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
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// The Library page (amendment §A3.4, Task G3-3), one page per tab with
// every card the view keeps since 2026-11-10 (owner: "We should probably
// not paginate the libraries so we can mass select and scroll more
// smoothly"), its custom filter builder's routes, and its stream.

// handleLibrary renders a tab's Library page from the current library
// projection returned by Options.Library, in the view its query names
// (libraryView), plus the toolbar's single "Rescan", live when a
// RootFolder of the tab's kinds exists (listRootFolders, narrowed by
// rootFoldersFor).
func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	tab, ok := projection.ParseTab(r.PathValue("tab"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	view := parseLibraryView(tab, r.URL.Query())
	all := projection.ForTab(s.opts.Library(r.Context()), tab)
	items := view.arrange(all)
	// The bar means nothing under any order but the title's, A to Z.
	var bar []views.Jump
	if view.Sort == projection.SortTitle && !view.Desc {
		bar = jumps(items)
	}
	rootFolders := rootFoldersFor(s.listRootFolders(r.Context()), tab)
	preloadable(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	out, done := compressed(w, r)
	defer done()
	base := "/library/" + string(tab)
	if err := views.Library(tab, items, view.toolbar(base, all, len(items)), rootFolders, bar).Render(r.Context(), out); err != nil {
		logging.FromContext(r.Context()).Error("render library page", "err", err)
	}
}

// compressed is w gzipped when the browser takes gzip: a tab's page is
// every card, about 5 KB of markup each and alike enough to shrink tens of
// times over. done flushes and closes the stream.
func compressed(w http.ResponseWriter, r *http.Request) (io.Writer, func()) {
	if !acceptsGzip(r) {
		return w, func() {}
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Add("Vary", "Accept-Encoding")
	gz, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if err != nil {
		w.Header().Del("Content-Encoding")
		return w, func() {}
	}
	return gz, func() { _ = gz.Close() }
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc, q, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(enc), "gzip") {
			return strings.TrimSpace(q) != "q=0"
		}
	}
	return false
}

// handleLibraryFilter applies the custom filter dialog: the view its rows
// ask for (viewFromBuilder), as the page's own URL -- a redirect, so the
// address bar, the stream and a shared link all carry the filter as ?f.
func (s *Server) handleLibraryFilter(w http.ResponseWriter, r *http.Request) {
	tab, ok := projection.ParseTab(r.PathValue("tab"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	view := viewFromBuilder(tab, r.URL.Query())
	http.Redirect(w, r, view.href("/library/"+string(tab)), http.StatusSeeOther)
}

// filterRowIndex is what a builder row's index may be: it names the row's
// fields and sits inside the row's hx-trigger expression, so nothing but
// letters and digits.
var filterRowIndex = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

// handleLibraryFilterRow renders one builder row: a new one, its index
// made here, for "Add condition"; or a row redrawn because its field or
// predicate changed (?row=<i> with the row's own fields), which keeps the
// predicate and values only while the field is the one it was drawn for
// (prev.<i>).
func (s *Server) handleLibraryFilterRow(w http.ResponseWriter, r *http.Request) {
	tab, ok := projection.ParseTab(r.PathValue("tab"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	index := q.Get("row")
	if !filterRowIndex.MatchString(index) {
		index = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	key := q.Get("field." + index)
	var op projection.Op
	var values []string
	if key != "" && key == q.Get("prev."+index) {
		op, values = projection.Op(q.Get("op."+index)), q["value."+index]
	}
	all := projection.ForTab(s.opts.Library(r.Context()), tab)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.FilterRowView(string(tab), filterRow(tab, all, index, key, op, values)).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render library filter row", "err", err)
	}
}

// maxCardsPerRequest bounds POST /library/{tab}/cards: more than any tab
// holds, so a cold page asking for every card still gets them.
const maxCardsPerRequest = 20000

// handleLibraryCards answers the stream's new and changed cards: the
// tab's cards named by the repeated "id" field (views.CardID), in that
// order, any the tab no longer holds left out. A read, posted only
// because a list of ids can outgrow a URL.
func (s *Server) handleLibraryCards(w http.ResponseWriter, r *http.Request) {
	tab, ok := projection.ParseTab(r.PathValue("tab"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	ids := r.PostForm["id"]
	if len(ids) > maxCardsPerRequest {
		ids = ids[:maxCardsPerRequest]
	}
	byID := map[string]projection.LibraryItem{}
	for _, li := range projection.ForTab(s.opts.Library(r.Context()), tab) {
		byID[views.CardID(li)] = li
	}
	items := make([]projection.LibraryItem, 0, len(ids))
	for _, id := range ids {
		if li, ok := byID[id]; ok {
			items = append(items, li)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	out, done := compressed(w, r)
	defer done()
	if err := views.LibraryCards(items).Render(r.Context(), out); err != nil {
		logging.FromContext(r.Context()).Error("render library cards", "err", err)
	}
}

// handleLibraryEvents streams a tab's Library page in the page's view.
// It sends no markup: whenever the cards the view keeps, their order or
// their fingerprints change, a library-manifest event lists them
// (libraryManifest), and ui/static/library.js fetches the new and changed
// ones from handleLibraryCards. One stream per tab, filtered per frame
// with the rule the page uses; the projection stays one tick.
func (s *Server) handleLibraryEvents(w http.ResponseWriter, r *http.Request) {
	tab, ok := projection.ParseTab(r.PathValue("tab"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()
	ch, unsubscribe := s.opts.SubscribeLibrary()
	defer unsubscribe()
	// The SSE art event (artwork design §B.8 as amended 2026-10-07): a
	// changed cover of an item on the page.
	art, unsubscribeArt := s.artEvents.subscribe()
	defer unsubscribeArt()
	onPage := map[string]bool{}

	view := parseLibraryView(tab, r.URL.Query())
	var last string
	for {
		select {
		case <-ctx.Done():
			return
		case items := <-ch:
			all := projection.ForTab(items, tab)
			rows := view.arrange(all)
			clear(onPage)
			for _, li := range rows {
				onPage[itemOfArtKey(li.ArtKey)] = true
			}
			m := libraryManifest(rows, len(all))
			if m == last {
				continue
			}
			if !writeSSEEvent(w, "library-manifest", m) {
				return
			}
			last = m
			flusher.Flush()
		case e := <-art:
			if !onPage[itemOfArtKey(e.Key)] {
				continue
			}
			if !writeArtEvent(w, e) {
				return
			}
			flusher.Flush()
		}
	}
}

// libraryManifest is a library-manifest event's data: "total <n>", the
// tab's item count, then a line per card the view keeps, in order, of its
// views.CardID and views.CardFingerprint.
func libraryManifest(rows []projection.LibraryItem, total int) string {
	var b strings.Builder
	b.Grow(16 + len(rows)*64)
	b.WriteString("total ")
	b.WriteString(strconv.Itoa(total))
	for _, li := range rows {
		b.WriteByte('\n')
		b.WriteString(views.CardID(li))
		b.WriteByte(' ')
		b.WriteString(views.CardFingerprint(li))
	}
	return b.String()
}

// writeSSEEvent writes one named event, its data a line per line of data.
func writeSSEEvent(w io.Writer, name, data string) bool {
	var buf bytes.Buffer
	buf.WriteString("event: ")
	buf.WriteString(name)
	buf.WriteByte('\n')
	for line := range strings.SplitSeq(data, "\n") {
		buf.WriteString("data: ")
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	buf.WriteByte('\n')
	_, err := w.Write(buf.Bytes())
	return err == nil
}
