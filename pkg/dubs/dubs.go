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

// Package dubs is the MAL-Dubs list (github.com/MAL-Dubs/MAL-Dubs): the
// MyAnimeList ids of anime with an English dub, complete ("dubbed") or still
// being dubbed ("incomplete", a simuldub). An anime wants English audio only
// when its MAL id is on it (owner, 2026-11-03: "We should update the logic to
// fetch english dubs (dual audio) *only* for series who's
// status.externalIDs.mal exists within" the list): of the 508 anime series
// on talos, 383 were dubbed, 2 incomplete, 97 not dubbed and 26 had no MAL
// id, and every one of the last 123 searched for an English dub and a donor
// to graft that could not exist.
//
// The manager's leader fetches the list once a day (Refresher) into the
// clustarr-mal-dubs bucket, and every binary that decides releases watches
// it into an Index on its root context (WithIndex, From), as
// pkg/grouplang's index is. decision.ForDub turns an item's State into its
// audio policy.
package dubs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

const (
	// URL is the list's raw file.
	URL = "https://raw.githubusercontent.com/MAL-Dubs/MAL-Dubs/main/data/dubInfo.json"
	// Key is the bucket's one entry, a Record.
	Key = "dubinfo"
	// RefreshInterval is how old the stored list may grow before the
	// leader fetches it again (conditionally, by its ETag).
	RefreshInterval = 24 * time.Hour
	// RetryInterval is how soon a failed fetch is tried again.
	RetryInterval = time.Hour
	// maxBody caps the fetched file: 61 KB on 2026-11-03.
	maxBody = 8 << 20
)

// ErrResponseTooLarge is a fetched list over maxBody.
var ErrResponseTooLarge = errors.New("dubs: response body exceeds size limit")

// State is what the list says of one item's English dub.
type State string

const (
	// Ungoverned is an item the list does not decide: not anime, or no list
	// read yet, so a fetch that never succeeded changes nothing. Its
	// profile's audio policy stands.
	Ungoverned State = ""
	// Dubbed is an anime whose MAL id the list names dubbed: English is
	// wanted as the profile says.
	Dubbed State = "dubbed"
	// Incomplete is an anime whose dub is still being made: English is
	// optional (decision.ForDub, quality.Profile.AudioOptional).
	Incomplete State = "incomplete"
	// NotDubbed is an anime the list does not name, or one without a MAL
	// id: English is not wanted.
	NotDubbed State = "notDubbed"
)

// Record is the bucket's entry: the list's two id sets and the fetch that
// produced them.
type Record struct {
	Dubbed     []int     `json:"dubbed"`
	Incomplete []int     `json:"incomplete"`
	ETag       string    `json:"etag,omitempty"`
	FetchedAt  time.Time `json:"fetchedAt"`
}

// Parse reads dubInfo.json: its "dubbed" and "incomplete" id lists, which
// are disjoint (an incomplete dub is not also listed dubbed). A file with
// neither is an error, so a changed format never reads as "nothing dubbed".
func Parse(body []byte) (dubbed, incomplete []int, err error) {
	var f struct {
		Dubbed     *[]int `json:"dubbed"`
		Incomplete *[]int `json:"incomplete"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, nil, fmt.Errorf("dubs: parse: %w", err)
	}
	if f.Dubbed == nil || f.Incomplete == nil {
		return nil, nil, errors.New("dubs: parse: the file names no dubbed or no incomplete list")
	}
	return *f.Dubbed, *f.Incomplete, nil
}

// Fetch gets the list at url, conditionally on etag ("" for none):
// notModified is a 304, which carries no Record.
func Fetch(ctx context.Context, hc *http.Client, url, etag string) (rec Record, notModified bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Record{}, false, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Record{}, false, fmt.Errorf("dubs: fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotModified:
		return Record{}, true, nil
	case resp.StatusCode != http.StatusOK:
		return Record{}, false, fmt.Errorf("dubs: fetch: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return Record{}, false, fmt.Errorf("dubs: fetch: %w", err)
	}
	if len(body) > maxBody {
		return Record{}, false, ErrResponseTooLarge
	}
	rec.Dubbed, rec.Incomplete, err = Parse(body)
	if err != nil {
		return Record{}, false, err
	}
	rec.ETag = resp.Header.Get("ETag")
	return rec, false, nil
}

// Index answers an item's State from the list it last read, and keeps what
// changed since TakeChange last ran, so the items whose State moved are
// planned again (the remediation loop's dubs source): an item's policy is
// read only when something wakes it.
type Index struct {
	mu                 sync.RWMutex
	dubbed, incomplete map[int]struct{}
	loaded             bool
	// pendingAll is the first list read: every anime moved from
	// Ungoverned. pending are the MAL ids whose State moved since.
	pendingAll bool
	pending    map[int]struct{}
	signal     chan struct{}
}

// NewIndex is an Index that has read no list: every item Ungoverned.
func NewIndex() *Index { return &Index{signal: make(chan struct{}, 1)} }

// Set replaces the index's list with r's, noting what changed.
func (ix *Index) Set(r Record) {
	d := make(map[int]struct{}, len(r.Dubbed))
	for _, id := range r.Dubbed {
		d[id] = struct{}{}
	}
	in := make(map[int]struct{}, len(r.Incomplete))
	for _, id := range r.Incomplete {
		in[id] = struct{}{}
	}
	ix.mu.Lock()
	changed := !ix.loaded
	if changed {
		ix.pendingAll = true
	} else {
		changed = ix.notePending(ix.dubbed, d) || changed
		changed = ix.notePending(d, ix.dubbed) || changed
		changed = ix.notePending(ix.incomplete, in) || changed
		changed = ix.notePending(in, ix.incomplete) || changed
	}
	ix.dubbed, ix.incomplete, ix.loaded = d, in, true
	ix.mu.Unlock()
	if changed {
		select {
		case ix.signal <- struct{}{}:
		default: // a change is already signalled; TakeChange reads them all
		}
	}
}

// notePending records the ids of a missing from b; it holds ix.mu.
func (ix *Index) notePending(a, b map[int]struct{}) bool {
	moved := false
	for id := range a {
		if _, ok := b[id]; !ok {
			if ix.pending == nil {
				ix.pending = map[int]struct{}{}
			}
			ix.pending[id] = struct{}{}
			moved = true
		}
	}
	return moved
}

// Changes signals (at most one pending) that the list changed since
// TakeChange last ran. A nil index never signals.
func (ix *Index) Changes() <-chan struct{} {
	if ix == nil {
		return nil
	}
	return ix.signal
}

// TakeChange is what changed since it last ran: all when the index read its
// first list (every anime left Ungoverned), else the MAL ids whose State
// moved.
func (ix *Index) TakeChange() (all bool, ids map[int]struct{}) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	all, ids = ix.pendingAll, ix.pending
	ix.pendingAll, ix.pending = false, nil
	return all, ids
}

// Moved reports whether malID's State is among a change TakeChange
// returned.
func Moved(all bool, ids map[int]struct{}, malID string) bool {
	if all {
		return true
	}
	id, err := strconv.Atoi(malID)
	if err != nil {
		return false
	}
	_, ok := ids[id]
	return ok
}

// Lookup is an anime's State by its MAL id ("" when it has none):
// Ungoverned until the index has read a list, then NotDubbed for no id or
// an id the list does not name.
func (ix *Index) Lookup(malID string) State {
	if ix == nil {
		return Ungoverned
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if !ix.loaded {
		return Ungoverned
	}
	id, err := strconv.Atoi(malID)
	if err != nil {
		return NotDubbed
	}
	if _, ok := ix.incomplete[id]; ok {
		return Incomplete
	}
	if _, ok := ix.dubbed[id]; ok {
		return Dubbed
	}
	return NotDubbed
}

// Series is s's State: Ungoverned unless it is an anime (spec.seriesType),
// else its MAL id's (status.metadata.externalIDs["mal"]).
func (ix *Index) Series(s *catalogv1alpha1.Series) State {
	if s == nil || s.Spec.SeriesType != catalogv1alpha1.SeriesTypeAnime {
		return Ungoverned
	}
	var mal string
	if md := s.Status.Metadata; md != nil {
		mal = md.ExternalIDs["mal"]
	}
	return ix.Lookup(mal)
}

// Movie is m's State: Ungoverned unless it was classified anime
// (status.classification, movie.Classify), else its MAL id's.
func (ix *Index) Movie(m *catalogv1alpha1.Movie) State {
	if m == nil || m.Status.Classification == nil || !m.Status.Classification.Anime {
		return Ungoverned
	}
	var mal string
	if md := m.Status.Metadata; md != nil {
		mal = md.ExternalIDs["mal"]
	}
	return ix.Lookup(mal)
}

// apply folds the bucket's entry into the index; a delete or an unreadable
// value leaves the list it had.
func (ix *Index) apply(e events.Entry) {
	var r Record
	if e.Key != Key || e.Value == nil || json.Unmarshal(e.Value, &r) != nil {
		return
	}
	ix.Set(r)
}

// rewatchDelay is how long Run waits to watch the bucket again after a
// watch failed or ended.
const rewatchDelay = 10 * time.Second

// Run watches the bucket into the index until ctx ends, watching again
// after a failure. It is a controller-runtime runnable's body.
func (ix *Index) Run(ctx context.Context, kv events.KV) error {
	log := logging.FromContext(ctx)
	for {
		ch, err := kv.Watch(ctx, Key)
		if err != nil {
			log.Warn("dubs: watching the MAL-Dubs list failed; retrying", "err", err)
		} else {
			for e := range ch {
				ix.apply(e)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(rewatchDelay):
		}
	}
}

type ctxKey struct{}

// WithIndex is ctx carrying ix, the process's MAL-Dubs index.
func WithIndex(ctx context.Context, ix *Index) context.Context {
	return context.WithValue(ctx, ctxKey{}, ix)
}

// From is the index ctx carries, nil (every item Ungoverned) when none.
func From(ctx context.Context) *Index {
	ix, _ := ctx.Value(ctxKey{}).(*Index)
	return ix
}

// Refresher keeps the bucket's Record within RefreshInterval of the list:
// the leader's, so one process fetches.
type Refresher struct {
	KV   events.KV
	HTTP *http.Client
	// URL is the list's address; empty is URL.
	URL string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Run refreshes the record until ctx ends: at once when it is missing or
// older than RefreshInterval, then when it next comes due, RetryInterval
// after a failure.
func (r *Refresher) Run(ctx context.Context) error {
	for {
		wait := r.refresh(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

// refresh fetches the list when the stored record is due, and is how long
// to wait before the next look.
func (r *Refresher) refresh(ctx context.Context) time.Duration {
	log := logging.FromContext(ctx)
	now := r.now()
	var cur Record
	e, err := r.KV.Get(ctx, Key)
	switch {
	case err == nil:
		if json.Unmarshal(e.Value, &cur) != nil {
			cur = Record{}
		}
	case !errors.Is(err, events.ErrKeyNotFound):
		log.Warn("dubs: reading the stored MAL-Dubs list failed", "err", err)
		return RetryInterval
	}
	if due := cur.FetchedAt.Add(RefreshInterval); !cur.FetchedAt.IsZero() && now.Before(due) {
		return due.Sub(now)
	}
	url := r.URL
	if url == "" {
		url = URL
	}
	etag := cur.ETag
	if cur.FetchedAt.IsZero() {
		etag = "" // nothing stored to keep on a 304
	}
	rec, notModified, err := Fetch(ctx, r.HTTP, url, etag)
	if err != nil {
		log.Warn("dubs: fetching the MAL-Dubs list failed; the stored list stands", "err", err)
		return RetryInterval
	}
	if notModified {
		rec = cur
	}
	rec.FetchedAt = now
	b, err := json.Marshal(rec)
	if err == nil {
		_, err = r.KV.Put(ctx, Key, b)
	}
	if err != nil {
		log.Warn("dubs: storing the MAL-Dubs list failed", "err", err)
		return RetryInterval
	}
	log.Info("dubs: MAL-Dubs list refreshed", "dubbed", len(rec.Dubbed), "incomplete", len(rec.Incomplete), "notModified", notModified)
	return RefreshInterval
}

func (r *Refresher) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}
