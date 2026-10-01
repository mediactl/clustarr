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

// Package projection is the process-wide pipeline projection loop (design
// plan docs/superpowers/plans/2026-09-22-phase-d3-ui.md, ruling R4).
//
// Before this package existed, ui/sse.go polled pkg/pipeline.Project on a
// ticker inside every /events/pipeline connection handler, so N open browser
// tabs relisted and re-projected the whole catalogue N times a tick.
// Projection.Run does it once, on its own ticker, and Subscribe fans the
// SAME computed slice out to every listener; a slow or gone listener drops a
// frame rather than stalling the loop for everyone else.
package projection

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	subtitlev1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/pipeline"
)

// DefaultInterval is how often a Projection recomputes when cmd/clustarr
// does not choose its own.
const DefaultInterval = 5 * time.Second

// Projection computes []pipeline.Entry over a client.Reader on a fixed
// interval and broadcasts the result to every current [Subscribe]r. The
// same tick also broadcasts the Download list gathered along the way (Task
// D3-3, ruling R4) to every current [SubscribeDownloads]r, plus -- Task
// G3-3, same ruling -- the Library page's []LibraryItem to every current
// [SubscribeLibrary]r and the Unmatched page's []UnmatchedEntry to every
// current [SubscribeUnmatched]r, plus -- Task G3-4, same ruling -- the
// Import Lists page's []ImportListEntry to every current
// [SubscribeImportLists]r, so all five streams come from one list round
// rather than each running its own.
type Projection struct {
	reader   client.Reader
	interval time.Duration
	// history is how many settled pipeline entries a round keeps
	// (pipeline.Trim); WithPipelineHistory sets it.
	history int

	// projected is set once the first round has completed; see Projected.
	projected atomic.Bool

	// tracked is set once every kind a round lists has an event handler on
	// the reader's informers (watchChanges): from then on dirty is true only
	// when something changed since the last round began. An untracked
	// reader -- a raw or fake client -- is always dirty, as every round was.
	tracked atomic.Bool
	dirty   atomic.Bool
	// round serialises rounds -- Run's ticks and a page read's on-demand
	// refresh -- and lastRound is when the newest completed one began.
	round     sync.Mutex
	lastRound atomic.Int64 // unix nanoseconds

	mu             sync.Mutex
	entries        []pipeline.Entry
	downloads      []downloadv1.Download
	library        []LibraryItem
	unmatched      []UnmatchedEntry
	importLists    []ImportListEntry
	subs           map[chan []pipeline.Entry]struct{}
	downloadSubs   map[chan []downloadv1.Download]struct{}
	librarySubs    map[chan []LibraryItem]struct{}
	unmatchedSubs  map[chan []UnmatchedEntry]struct{}
	importListSubs map[chan []ImportListEntry]struct{}
}

// New builds a Projection over r, recomputed every interval once [Run] is
// called. r may be nil -- see ui.Options.Reader's own doc comment -- in
// which case every tick yields no rows rather than reaching for a client
// that does not exist; [Entries] and [Subscribe] both stay legal to call,
// they just never see any rows. Nothing here starts anything; call [Run] to
// begin projecting.
func New(r client.Reader, interval time.Duration, opts ...Option) *Projection {
	p := &Projection{
		reader:         r,
		interval:       interval,
		history:        DefaultPipelineHistory,
		subs:           make(map[chan []pipeline.Entry]struct{}),
		downloadSubs:   make(map[chan []downloadv1.Download]struct{}),
		librarySubs:    make(map[chan []LibraryItem]struct{}),
		unmatchedSubs:  make(map[chan []UnmatchedEntry]struct{}),
		importListSubs: make(map[chan []ImportListEntry]struct{}),
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// DefaultPipelineHistory is how many results the pipeline page keeps
// beside its in-flight entries: two pages at paging.DefaultPer. The page
// is for what is happening and what just finished; the history stream is
// the record.
const DefaultPipelineHistory = 100

// Option configures a Projection.
type Option func(*Projection)

// WithPipelineHistory keeps the n newest settled pipeline entries
// (pipeline.Trim); 0 keeps in-flight entries only.
func WithPipelineHistory(n int) Option {
	return func(p *Projection) { p.history = n }
}

// Run computes the projection immediately and then, every interval, again
// when something changed and a stream is open (see tick), broadcasting each
// result to every current subscriber, until ctx is cancelled. It returns ctx.Err() once that happens. A List failure against
// the cluster during one tick is logged and that tick is skipped -- the
// previous snapshot keeps serving -- rather than tearing down every open
// page on one transient cluster hiccup; Run itself only stops via ctx.
func (p *Projection) Run(ctx context.Context) error {
	p.watchChanges(ctx)
	p.tick(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			p.tick(ctx)
		}
	}
}

// tick runs a projection round when one is worth running: always until
// the first round has completed (ui's /readyz waits on [Projected], and a
// failed round must be retried with or without a subscriber), and after
// that only when something changed since the last round began AND a
// stream is open to receive the result. With no stream open a change
// waits for the next page read ([Projection.Library] and its siblings),
// which projects it on demand. Over the ui's cache an idle library is
// therefore never listed again, and a busy one only while someone watches.
func (p *Projection) tick(ctx context.Context) {
	if p.Projected() && (!p.isDirty() || !p.hasSubscribers()) {
		return
	}
	p.runRound(ctx, func() bool { return !p.Projected() || (p.isDirty() && p.hasSubscribers()) })
}

// refresh is a page read's on-demand round: once the first round has
// landed, a change older than one interval is projected before the read
// answers, so a page is never staler than one interval -- the bound every
// read had when each tick rebuilt -- although no tick rebuilt for it.
// Before the first round it does nothing: Run's own first round is in
// flight, and a read before it has always answered empty.
func (p *Projection) refresh(ctx context.Context) {
	due := func() bool {
		return p.Projected() && p.isDirty() &&
			time.Since(time.Unix(0, p.lastRound.Load())) >= p.interval
	}
	if due() {
		p.runRound(ctx, due)
	}
}

// isDirty reports whether a round could see something the last one did
// not: always for an untracked reader.
func (p *Projection) isDirty() bool { return !p.tracked.Load() || p.dirty.Load() }

func (p *Projection) hasSubscribers() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.subs)+len(p.downloadSubs)+len(p.librarySubs)+len(p.unmatchedSubs)+len(p.importListSubs) > 0
}

// runRound computes one projection round -- one round of List calls -- and
// publishes every result (the pipeline entries and, per ruling R4, the
// downloads, library, unmatched and import-list slices gathered in the same
// round) to every subscriber of each. Rounds are serialised, and still runs
// only if due still holds once this caller has the round to itself, so a
// page read that waited on a tick's round does not repeat it.
//
// dirty is cleared before the first List, not after the last: a change
// landing while the round lists re-marks it, and the next round projects
// it. A failed round marks it again.
func (p *Projection) runRound(ctx context.Context, due func() bool) {
	p.round.Lock()
	defer p.round.Unlock()
	if !due() {
		return
	}
	started := time.Now()
	p.dirty.Store(false)
	entries, downloads, library, unmatched, importLists, err := p.project(ctx)
	if err != nil {
		p.dirty.Store(true)
		logging.FromContext(ctx).Error("compute pipeline projection", "error", err)
		return
	}
	p.lastRound.Store(started.UnixNano())

	p.mu.Lock()
	defer p.mu.Unlock()
	defer p.projected.Store(true)
	p.entries = entries
	p.downloads = downloads
	p.library = library
	p.unmatched = unmatched
	p.importLists = importLists
	for ch := range p.subs {
		publish(ch, entries)
	}
	for ch := range p.downloadSubs {
		publish(ch, downloads)
	}
	for ch := range p.librarySubs {
		publish(ch, library)
	}
	for ch := range p.unmatchedSubs {
		publish(ch, unmatched)
	}
	for ch := range p.importListSubs {
		publish(ch, importLists)
	}
}

// Projected reports whether a projection round has completed, so every
// page and stream has something to serve. ui's /readyz gates on it
// (ui.Options.Projected): a cache that has synced is not yet a page with
// rows, and before this gate a fresh ui pod reported Ready and served empty
// pages until its first round landed. A round that fails does not count --
// the previous snapshot, if any, keeps serving -- and with a nil reader the
// first round completes at once, with no rows, so a ui with no cluster
// still becomes Ready.
func (p *Projection) Projected() bool { return p.projected.Load() }

// publish delivers v to ch without blocking. A slow subscriber -- an SSE
// connection whose client stopped reading -- gets its stale, buffered frame
// replaced by the newest one instead of stalling the whole projection loop:
// "a slow consumer drops frames rather than blocking the loop; the newest
// projection is the only one worth delivering" (design plan, Task D3-1).
// It is generic over the payload so both the pipeline broadcast ([]pipeline.
// Entry) and the downloads broadcast ([]downloadv1.Download, Task D3-3)
// share the one implementation.
func publish[T any](ch chan T, v T) {
	select {
	case ch <- v:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- v:
	default:
	}
}

// Entries returns the most recently computed projection. It never blocks on
// the cluster: [Run] populates it in the background, and a Projection that
// has not ticked yet (or was built over a nil Reader) returns nil, which
// every caller here -- ui.Server among them -- already treats the same as
// "no rows". Over the ui's cache a read may first project a change the
// ticks left for it (see refresh): from memory, never from the cluster.
// The other accessors below do the same.
func (p *Projection) Entries(ctx context.Context) []pipeline.Entry {
	p.refresh(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.entries
}

// Downloads returns the most recently computed downloads slice -- the
// Task D3-3 analogue of [Entries] for the same reasons: it never blocks on
// the cluster, and a Projection that has not ticked yet (or was built over
// a nil Reader) returns nil.
func (p *Projection) Downloads(ctx context.Context) []downloadv1.Download {
	p.refresh(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.downloads
}

// Library returns the most recently computed library slice -- the Task
// G3-3 analogue of [Entries] for the Library page, for the same reasons: it
// never blocks on the cluster, and a Projection that has not ticked yet (or
// was built over a nil Reader) returns nil.
func (p *Projection) Library(ctx context.Context) []LibraryItem {
	p.refresh(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.library
}

// Unmatched returns the most recently computed unmatched-files slice -- the
// Task G3-3 analogue of [Entries] for the Unmatched page, for the same
// reasons: it never blocks on the cluster, and a Projection that has not
// ticked yet (or was built over a nil Reader) returns nil.
func (p *Projection) Unmatched(ctx context.Context) []UnmatchedEntry {
	p.refresh(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.unmatched
}

// ImportLists returns the most recently computed import-list slice -- the
// Task G3-4 analogue of [Entries] for the Import Lists page, for the same
// reasons: it never blocks on the cluster, and a Projection that has not
// ticked yet (or was built over a nil Reader) returns nil.
func (p *Projection) ImportLists(ctx context.Context) []ImportListEntry {
	p.refresh(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.importLists
}

// Subscribe registers a new listener and returns a channel that receives
// the current projection immediately, and again every time [Run] recomputes
// it, plus a func that unsubscribes. The caller must call the returned func
// exactly once when done listening (typically deferred) or the channel and
// its slot in the subscriber set leak for the life of the Projection.
func (p *Projection) Subscribe() (<-chan []pipeline.Entry, func()) {
	ch := make(chan []pipeline.Entry, 1)

	p.mu.Lock()
	ch <- p.entries
	p.subs[ch] = struct{}{}
	p.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			p.mu.Lock()
			delete(p.subs, ch)
			p.mu.Unlock()
		})
	}
	return ch, unsubscribe
}

// SubscribeDownloads is [Subscribe]'s downloads-stream counterpart (Task
// D3-3): same immediate-then-on-change delivery, same single-call
// unsubscribe, but fed from the Download list [tick] already gathers for
// [Subscribe] -- ruling R4's "one list round feeds both streams" -- rather
// than a List call of its own.
func (p *Projection) SubscribeDownloads() (<-chan []downloadv1.Download, func()) {
	ch := make(chan []downloadv1.Download, 1)

	p.mu.Lock()
	ch <- p.downloads
	p.downloadSubs[ch] = struct{}{}
	p.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			p.mu.Lock()
			delete(p.downloadSubs, ch)
			p.mu.Unlock()
		})
	}
	return ch, unsubscribe
}

// SubscribeLibrary is [Subscribe]'s Library-page counterpart (Task G3-3):
// same immediate-then-on-change delivery, same single-call unsubscribe, but
// fed from the catalog items [tick] already lists for [Subscribe] -- ruling
// R4's "one list round feeds every stream" -- rather than a List call of its
// own.
func (p *Projection) SubscribeLibrary() (<-chan []LibraryItem, func()) {
	ch := make(chan []LibraryItem, 1)

	p.mu.Lock()
	ch <- p.library
	p.librarySubs[ch] = struct{}{}
	p.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			p.mu.Lock()
			delete(p.librarySubs, ch)
			p.mu.Unlock()
		})
	}
	return ch, unsubscribe
}

// SubscribeUnmatched is [Subscribe]'s Unmatched-page counterpart (Task
// G3-3): same immediate-then-on-change delivery, same single-call
// unsubscribe, fed from the one additional LibraryScan List call [project]
// adds to the shared tick.
func (p *Projection) SubscribeUnmatched() (<-chan []UnmatchedEntry, func()) {
	ch := make(chan []UnmatchedEntry, 1)

	p.mu.Lock()
	ch <- p.unmatched
	p.unmatchedSubs[ch] = struct{}{}
	p.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			p.mu.Lock()
			delete(p.unmatchedSubs, ch)
			p.mu.Unlock()
		})
	}
	return ch, unsubscribe
}

// SubscribeImportLists is [Subscribe]'s Import Lists page counterpart (Task
// G3-4): same immediate-then-on-change delivery, same single-call
// unsubscribe, fed from the one additional ImportList List call [project]
// adds to the shared tick.
func (p *Projection) SubscribeImportLists() (<-chan []ImportListEntry, func()) {
	ch := make(chan []ImportListEntry, 1)

	p.mu.Lock()
	ch <- p.importLists
	p.importListSubs[ch] = struct{}{}
	p.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			p.mu.Lock()
			delete(p.importListSubs, ch)
			p.mu.Unlock()
		})
	}
	return ch, unsubscribe
}

// informerSource is what the ui's reader -- ui.NewClusterReader's
// informer-backed cache, handed over as a client.Reader -- offers beyond
// reads: an informer per kind to hang a change handler on. Registering one
// writes nothing.
type informerSource interface {
	GetInformer(ctx context.Context, obj client.Object, opts ...cache.InformerGetOption) (cache.Informer, error)
}

// watchedKinds is every kind a round lists (listItems, buildRelatedIndex,
// listLibraryScans, listImportLists); a change to any of them marks the
// projection dirty. TestEveryListedKindIsWatched holds the two together.
func watchedKinds() []client.Object {
	return []client.Object{
		&catalogv1.Movie{}, &catalogv1.Series{}, &catalogv1.Episode{},
		&catalogv1.Album{}, &catalogv1.Artist{}, &catalogv1.Author{},
		&catalogv1.Book{}, &catalogv1.Audiobook{}, &catalogv1.Comic{}, &catalogv1.Issue{},
		&catalogv1.MediaFile{}, &downloadv1.Download{}, &catalogv1.Search{},
		&transcodev1.TranscodeJob{}, &subtitlev1.SubtitleRequest{},
		&catalogv1.LibraryScan{}, &catalogv1.ImportList{},
	}
}

// watchChanges hangs a handler that marks the projection dirty on the
// informer of every kind a round lists, when the reader has informers.
// Registered before the first round, so no change after it is missed (an
// informer's initial list arrives as adds and marks it too). Any failure
// leaves the projection untracked -- always dirty, every tick with a
// subscriber a round, as before -- rather than trusting a partial watch.
func (p *Projection) watchChanges(ctx context.Context) {
	src, ok := p.reader.(informerSource)
	if !ok {
		return
	}
	mark := func() { p.dirty.Store(true) }
	handler := toolscache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { mark() },
		UpdateFunc: func(any, any) { mark() },
		DeleteFunc: func(any) { mark() },
	}
	for _, obj := range watchedKinds() {
		// Not blocking on the sync: the first round's Lists wait for it.
		inf, err := src.GetInformer(ctx, obj, cache.BlockUntilSynced(false))
		if err == nil {
			_, err = inf.AddEventHandler(handler)
		}
		if err != nil {
			logging.FromContext(ctx).Error("watch for projection changes; projecting every tick instead",
				"kind", fmt.Sprintf("%T", obj), "error", err)
			return
		}
	}
	p.dirty.Store(true)
	p.tracked.Store(true)
}

// listOpts is every round's List options. client.UnsafeDisableDeepCopy
// hands back the informer cache's own objects (a raw or fake client
// ignores it and copies as before): a round only reads them, and nothing
// it publishes writes through them -- LibraryItem and pipeline.Entry are
// plain values; the Download, UnmatchedEntry and ImportListEntry slices a
// stream or page renders share the cache's nested slices and pointers,
// which no renderer modifies (sorts reorder the round's own slices, never
// an object's fields). The objects of one round used to be a deep copy of
// the whole library every five seconds.
var listOpts = []client.ListOption{client.UnsafeDisableDeepCopy}

// project lists every catalog kind pkg/pipeline's describeItem handles plus
// everything index.go's buildRelatedIndex needs, then calls pipeline.Project
// once per catalog item. It also returns the Download list buildRelatedIndex
// gathered along the way (ruling R4: no second List call for the downloads
// stream), the Library page's []LibraryItem derived from the same items and
// entries (Task G3-3, again no second List call), the Unmatched page's
// []UnmatchedEntry from one additional LibraryScan List call, and the
// Import Lists page's []ImportListEntry from one additional ImportList List
// call (Task G3-4) -- each the one new List its own task added to the
// shared round, rather than a ticker of its own. A nil reader (no cluster
// configured) yields no rows without listing anything.
func (p *Projection) project(
	ctx context.Context,
) ([]pipeline.Entry, []downloadv1.Download, []LibraryItem, []UnmatchedEntry, []ImportListEntry, error) {
	if p.reader == nil {
		return nil, nil, nil, nil, nil, nil
	}

	idx, err := buildRelatedIndex(ctx, p.reader, listOpts...)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	items, err := p.listItems(ctx)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	entries := make([]pipeline.Entry, 0, len(items))
	for _, item := range items {
		entries = append(entries, pipeline.Project(item, idx.Related(item.GetUID())))
	}

	library := buildLibraryItems(items, entries)

	// Stages are evaluated in reverse-completion order internally
	// (pipeline.Project's own doc comment); the page itself sorts rows
	// newest-first by when each entered its current stage, so whatever just
	// moved is at the top.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Since.After(entries[j].Since) })
	// Then only the in-flight entries and the newest results
	// (--pipeline-history): one entry per catalog item was ~16,000 rows.
	entries = pipeline.Trim(entries, p.history)

	// Sorted by name for the same reason ui/routes.go's listDownloads sorts
	// its own, independent List the same way: a stable render, here across
	// successive SSE frames rather than across requests.
	downloads := idx.AllDownloads()
	sort.Slice(downloads, func(i, j int) bool { return downloads[i].Name < downloads[j].Name })

	scans, err := listLibraryScans(ctx, p.reader, listOpts...)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	unmatched := unmatchedFromScans(scans)

	lists, err := listImportLists(ctx, p.reader, listOpts...)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	importLists := buildImportListEntries(lists)

	return entries, downloads, library, unmatched, importLists, nil
}

// listItems lists the ten catalog kinds pkg/pipeline/project.go's
// describeItem type-switches over (Movie, Series, Episode, Album, Artist,
// Author, Book, Audiobook, Comic, Issue) and returns every item as a
// client.Object, so project can hand each one straight to pipeline.Project.
func (p *Projection) listItems(ctx context.Context) ([]client.Object, error) {
	var items []client.Object

	var movies catalogv1.MovieList
	if err := p.reader.List(ctx, &movies, listOpts...); err != nil {
		return nil, fmt.Errorf("projection: list movies: %w", err)
	}
	for i := range movies.Items {
		items = append(items, &movies.Items[i])
	}

	var series catalogv1.SeriesList
	if err := p.reader.List(ctx, &series, listOpts...); err != nil {
		return nil, fmt.Errorf("projection: list series: %w", err)
	}
	for i := range series.Items {
		items = append(items, &series.Items[i])
	}

	var episodes catalogv1.EpisodeList
	if err := p.reader.List(ctx, &episodes, listOpts...); err != nil {
		return nil, fmt.Errorf("projection: list episodes: %w", err)
	}
	for i := range episodes.Items {
		items = append(items, &episodes.Items[i])
	}

	var albums catalogv1.AlbumList
	if err := p.reader.List(ctx, &albums, listOpts...); err != nil {
		return nil, fmt.Errorf("projection: list albums: %w", err)
	}
	for i := range albums.Items {
		items = append(items, &albums.Items[i])
	}

	var artists catalogv1.ArtistList
	if err := p.reader.List(ctx, &artists, listOpts...); err != nil {
		return nil, fmt.Errorf("projection: list artists: %w", err)
	}
	for i := range artists.Items {
		items = append(items, &artists.Items[i])
	}

	var authors catalogv1.AuthorList
	if err := p.reader.List(ctx, &authors, listOpts...); err != nil {
		return nil, fmt.Errorf("projection: list authors: %w", err)
	}
	for i := range authors.Items {
		items = append(items, &authors.Items[i])
	}

	var books catalogv1.BookList
	if err := p.reader.List(ctx, &books, listOpts...); err != nil {
		return nil, fmt.Errorf("projection: list books: %w", err)
	}
	for i := range books.Items {
		items = append(items, &books.Items[i])
	}

	var audiobooks catalogv1.AudiobookList
	if err := p.reader.List(ctx, &audiobooks, listOpts...); err != nil {
		return nil, fmt.Errorf("projection: list audiobooks: %w", err)
	}
	for i := range audiobooks.Items {
		items = append(items, &audiobooks.Items[i])
	}

	var comics catalogv1.ComicList
	if err := p.reader.List(ctx, &comics, listOpts...); err != nil {
		return nil, fmt.Errorf("projection: list comics: %w", err)
	}
	for i := range comics.Items {
		items = append(items, &comics.Items[i])
	}

	var issues catalogv1.IssueList
	if err := p.reader.List(ctx, &issues, listOpts...); err != nil {
		return nil, fmt.Errorf("projection: list issues: %w", err)
	}
	for i := range issues.Items {
		items = append(items, &issues.Items[i])
	}

	return items, nil
}
