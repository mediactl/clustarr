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

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/jonboulle/clockwork"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/trailers"
	"github.com/mediactl/clustarr/pkg/ytdlp"
)

const (
	// DefaultInterval is how long after one pass over the catalog the
	// next starts: an item added now has its trailer before its file is
	// imported and Plex first asks for its extras.
	DefaultInterval = 2 * time.Minute
	// DefaultPace is the wait after each fetch, so the site sees a
	// trickle.
	DefaultPace = 20 * time.Second
	// throttlePause is how long nothing is fetched after the site refused
	// this client.
	throttlePause = time.Hour
	// verifyEvery is how often a ready trailer's file is looked for.
	verifyEvery = 6 * time.Hour
)

// Facts are what a trailer file holds.
type Facts struct {
	SizeBytes      int64
	DurationMillis int64
	Width, Height  int
	VideoCodec     string
	AudioCodec     string
}

// Sweeper passes over the catalog and keeps each item's trailer record.
// Every seam is a function, so a test runs it with no cluster, bus,
// program or FFmpeg.
type Sweeper struct {
	// Reader lists Movies, Series and RootFolders (the agent's cache).
	Reader    client.Reader
	Namespace string
	// KV is the clustarr-trailers bucket; the sweeper is its only writer.
	KV events.KV
	// PlexExtras answers Plex's own extras for a Plex id
	// (rpc.catalog.metadata.extras), and Videos the videos the trailer
	// providers name (rpc.catalog.metadata.trailers).
	PlexExtras func(ctx context.Context, plexID string) ([]json.RawMessage, error)
	Videos     func(ctx context.Context, kind commonv1.MediaKind, ids map[string]string) ([]trailers.Video, error)
	// Fetch fetches a video's page into dir; Mux joins a picture and a
	// sound into an MP4; Probe reads a finished file; Place moves it onto
	// the media volume.
	Fetch func(ctx context.Context, url, dir string) (ytdlp.Fetched, error)
	Mux   func(ctx context.Context, video, audio, output string) error
	Probe func(ctx context.Context, path string) (Facts, error)
	Place func(src, dst string) error
	// ScratchDir is where a fetch is assembled; Language is the language a
	// trailer is preferred in.
	ScratchDir string
	Language   string
	Interval   time.Duration
	Pace       time.Duration
	Clock      clockwork.Clock

	pausedUntil time.Time
	verified    map[string]time.Time
	// records is every record read or written, by key: the sweeper is the
	// bucket's only writer, so what it holds is what the bucket holds, and
	// a pass asks the bucket only for an item it has not seen.
	records    map[string]record
	lastForget time.Time
}

// record is a stored entry and the revision it was read or written at;
// absent is a key the bucket does not hold.
type record struct {
	entry  trailers.Entry
	rev    uint64
	absent bool
}

// forgetEvery is how often the records of gone items are looked for.
const forgetEvery = time.Hour

// item is a Movie or Series as the sweeper reads it.
type item struct {
	kind       string
	mediaKind  commonv1.MediaKind
	uid, name  string
	ids        map[string]string
	plexIDs    []string // its own, then its seasons'
	rootFolder string
	wanted     bool // monitored, or holding a file
	resolved   bool // its metadata has been fetched
}

// Run passes over the catalog until ctx ends.
func (s *Sweeper) Run(ctx context.Context) error {
	ctx = logging.Subsystem(ctx, "trailers.agent")
	log := logging.FromContext(ctx)
	interval := s.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	for {
		if err := s.Pass(ctx); err != nil && ctx.Err() == nil {
			log.WarnContext(ctx, "trailers: a pass over the catalog failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-s.Clock.After(interval):
		}
	}
}

// Pass looks at every Movie and Series once, then removes the records and
// files of items that are gone.
func (s *Sweeper) Pass(ctx context.Context) error {
	items, roots, err := s.catalog(ctx)
	if err != nil {
		return err
	}
	live := map[string]bool{}
	for _, it := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		live[trailers.Key(it.kind, it.uid)] = true
		s.look(ctx, it, roots)
	}
	// An empty catalog is a cache that has nothing yet, never a reason to
	// remove every trailer.
	if now := s.Clock.Now(); len(items) > 0 && now.Sub(s.lastForget) >= forgetEvery {
		s.lastForget = now
		return s.forget(ctx, live, roots)
	}
	return nil
}

// catalog lists the items and the root folders' paths by name.
func (s *Sweeper) catalog(ctx context.Context) ([]item, map[string]string, error) {
	var folders catalogv1.RootFolderList
	if err := s.Reader.List(ctx, &folders, client.InNamespace(s.Namespace)); err != nil {
		return nil, nil, fmt.Errorf("list root folders: %w", err)
	}
	roots := map[string]string{}
	for i := range folders.Items {
		roots[folders.Items[i].Name] = folders.Items[i].Spec.Path
	}
	var movies catalogv1.MovieList
	if err := s.Reader.List(ctx, &movies, client.InNamespace(s.Namespace)); err != nil {
		return nil, nil, fmt.Errorf("list movies: %w", err)
	}
	var series catalogv1.SeriesList
	if err := s.Reader.List(ctx, &series, client.InNamespace(s.Namespace)); err != nil {
		return nil, nil, fmt.Errorf("list series: %w", err)
	}
	items := make([]item, 0, len(movies.Items)+len(series.Items))
	for i := range movies.Items {
		m := &movies.Items[i]
		it := item{
			kind: trailers.KindMovie, mediaKind: commonv1.MediaKindMovie, uid: string(m.UID), name: m.Name,
			rootFolder: m.Spec.RootFolderRef, wanted: m.Status.HasFile || (m.Spec.Monitored != nil && *m.Spec.Monitored),
		}
		if md := m.Status.Metadata; md != nil {
			it.resolved, it.ids = true, md.ExternalIDs
			if id := md.ExternalIDs["plex"]; id != "" {
				it.plexIDs = []string{id}
			}
		}
		items = append(items, it)
	}
	for i := range series.Items {
		sr := &series.Items[i]
		it := item{
			kind: trailers.KindSeries, mediaKind: commonv1.MediaKindSeries, uid: string(sr.UID), name: sr.Name,
			rootFolder: sr.Spec.RootFolderRef, wanted: sr.Status.EpisodeFileCount > 0 || (sr.Spec.Monitored != nil && *sr.Spec.Monitored),
		}
		if md := sr.Status.Metadata; md != nil {
			it.resolved, it.ids = true, md.ExternalIDs
			if id := md.ExternalIDs["plex"]; id != "" {
				it.plexIDs = []string{id}
			}
			// Plex keeps most TV trailers on a season, and the provider's
			// extras route answers them with the show's own.
			seasons := slices.Clone(md.PlexSeasons)
			slices.SortStableFunc(seasons, func(a, b catalogv1.PlexSeasonRef) int { return int(a.Number - b.Number) })
			for _, p := range seasons {
				if p.Number > 0 && p.ID != "" {
					it.plexIDs = append(it.plexIDs, p.ID)
				}
			}
		}
		items = append(items, it)
	}
	return items, roots, nil
}

// look brings one item's record up to date. A failure is logged and the
// item is looked at again on the next pass.
func (s *Sweeper) look(ctx context.Context, it item, roots map[string]string) {
	log := logging.FromContext(ctx).With("kind", it.kind, "item", it.name)
	root := roots[it.rootFolder]
	if !it.resolved || !it.wanted || root == "" {
		return
	}
	key := trailers.Key(it.kind, it.uid)
	now := s.Clock.Now()
	entry, rev, have, err := s.read(ctx, key)
	if err != nil {
		log.WarnContext(ctx, "trailers: read the item's record", "err", err)
		return
	}
	if have && entry.State == trailers.StateReady {
		if s.present(key, entry.Path, now) {
			return
		}
		log.InfoContext(ctx, "trailers: the item's trailer file is gone; it is fetched again", "path", entry.Path)
	} else if have && !entry.Due(now) {
		return
	}

	next, err := s.decide(ctx, log, it, root, entry)
	if err != nil {
		if !errors.Is(err, errPaused) && ctx.Err() == nil {
			log.WarnContext(ctx, "trailers: the item could not be decided; it is looked at again next pass", "err", err)
		}
		return
	}
	next.CheckedAt = s.Clock.Now()
	if err := s.write(ctx, key, next, rev, have); err != nil {
		log.WarnContext(ctx, "trailers: write the item's record", "err", err)
		return
	}
	if next.State == trailers.StateReady {
		s.markPresent(key, next.CheckedAt)
		log.InfoContext(ctx, "trailers: a trailer was fetched", "path", next.Path, "site", next.Video.Site, "provider", next.Video.Provider,
			"height", next.Height, "seconds", next.DurationMillis/1000, "bytes", next.SizeBytes)
	}
}

// errPaused: nothing is fetched while the site is refusing this client.
var errPaused = errors.New("trailers: fetching is paused")

// decide is the item's next record: Plex has a trailer, no provider names
// one, or one was fetched or failed to be. An error is what could not be
// known, and writes nothing.
func (s *Sweeper) decide(ctx context.Context, log *slog.Logger, it item, root string, prev trailers.Entry) (trailers.Entry, error) {
	for _, id := range it.plexIDs {
		extras, err := s.PlexExtras(ctx, id)
		if err != nil {
			return trailers.Entry{}, fmt.Errorf("ask for Plex's extras of %s: %w", id, err)
		}
		if hasTrailer(extras) {
			return trailers.Entry{State: trailers.StatePlex}, nil
		}
	}
	videos, err := s.Videos(ctx, it.mediaKind, it.ids)
	if err != nil {
		return trailers.Entry{}, fmt.Errorf("ask which videos are named: %w", err)
	}
	video, ok := trailers.Pick(videos, s.Language)
	if !ok {
		return trailers.Entry{State: trailers.StateNone}, nil
	}
	if now := s.Clock.Now(); now.Before(s.pausedUntil) {
		return trailers.Entry{}, errPaused
	}

	dst := trailers.File(root, it.kind, it.uid)
	facts, err := s.fetch(ctx, video, dst)
	select {
	case <-ctx.Done():
	case <-s.Clock.After(s.pace()):
	}
	switch {
	case err == nil:
		e := trailers.Entry{
			State: trailers.StateReady, Video: &video, Path: dst,
			SizeBytes: facts.SizeBytes, DurationMillis: facts.DurationMillis, Width: facts.Width, Height: facts.Height,
			VideoCodec: facts.VideoCodec, AudioCodec: facts.AudioCodec,
		}
		return e, nil
	case ctx.Err() != nil:
		return trailers.Entry{}, ctx.Err()
	case errors.Is(err, ytdlp.ErrThrottled):
		s.pausedUntil = s.Clock.Now().Add(throttlePause)
		log.WarnContext(ctx, "trailers: the site is refusing this client; nothing is fetched for a while", "until", s.pausedUntil, "err", err)
		return trailers.Entry{}, errPaused
	}
	attempts := 1
	if prev.State == trailers.StateFailed && prev.Video != nil && prev.Video.Key == video.Key {
		attempts = prev.Attempts + 1
	}
	if errors.Is(err, ytdlp.ErrUnavailable) {
		// The page has no video and will not grow one.
		attempts = trailers.MaxAttempts
	}
	log.WarnContext(ctx, "trailers: the fetch failed", "site", video.Site, "attempt", attempts, "err", err)
	return trailers.Entry{
		State: trailers.StateFailed, Video: &video, Attempts: attempts, Error: err.Error(),
		RetryAt: s.Clock.Now().Add(trailers.RetryAfter(attempts)),
	}, nil
}

func (s *Sweeper) pace() time.Duration {
	if s.Pace > 0 {
		return s.Pace
	}
	return DefaultPace
}

// fetch fetches video's page, joins what came apart, and places the MP4 at
// dst. Everything is assembled in a scratch directory of its own.
func (s *Sweeper) fetch(ctx context.Context, video trailers.Video, dst string) (Facts, error) {
	url := video.URL()
	if url == "" {
		return Facts{}, fmt.Errorf("%s video %q has no page to fetch", video.Site, video.Key)
	}
	dir, err := os.MkdirTemp(s.ScratchDir, "trailer-")
	if err != nil {
		return Facts{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	got, err := s.Fetch(ctx, url, dir)
	if err != nil {
		return Facts{}, err
	}
	out := got.Video
	if got.Audio != "" {
		out = filepath.Join(dir, "trailer.mp4")
		if err := s.Mux(ctx, got.Video, got.Audio, out); err != nil {
			return Facts{}, fmt.Errorf("join the picture and the sound: %w", err)
		}
	}
	facts, err := s.Probe(ctx, out)
	if err != nil {
		return Facts{}, fmt.Errorf("read the fetched file: %w", err)
	}
	if facts.DurationMillis <= 0 || facts.VideoCodec == "" || facts.AudioCodec == "" {
		return Facts{}, fmt.Errorf("the fetched file is not a video with sound (%d ms, video %q, audio %q)", facts.DurationMillis, facts.VideoCodec, facts.AudioCodec)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o775); err != nil {
		return Facts{}, err
	}
	if err := s.Place(out, dst); err != nil {
		return Facts{}, fmt.Errorf("place the trailer: %w", err)
	}
	return facts, nil
}

// hasTrailer is whether Plex's extras hold a trailer.
func hasTrailer(extras []json.RawMessage) bool {
	return slices.ContainsFunc(extras, trailers.IsPlexTrailer)
}

// present is whether a ready trailer's file is where the record says,
// looked for once every verifyEvery.
func (s *Sweeper) present(key, path string, now time.Time) bool {
	if at, ok := s.verified[key]; ok && now.Sub(at) < verifyEvery {
		return true
	}
	if _, err := os.Stat(path); err != nil {
		// Only a file that is known gone is fetched again: a volume that
		// does not answer says nothing about the file.
		if !errors.Is(err, os.ErrNotExist) {
			return true
		}
		delete(s.verified, key)
		return false
	}
	s.markPresent(key, now)
	return true
}

func (s *Sweeper) markPresent(key string, at time.Time) {
	if s.verified == nil {
		s.verified = map[string]time.Time{}
	}
	s.verified[key] = at
}

func (s *Sweeper) read(ctx context.Context, key string) (trailers.Entry, uint64, bool, error) {
	if r, ok := s.records[key]; ok {
		return r.entry, r.rev, !r.absent, nil
	}
	e, err := s.KV.Get(ctx, key)
	if errors.Is(err, events.ErrKeyNotFound) {
		s.remember(key, record{absent: true})
		return trailers.Entry{}, 0, false, nil
	}
	if err != nil {
		return trailers.Entry{}, 0, false, err
	}
	// An unreadable record is an empty one: decided again and replaced.
	entry, _ := trailers.Decode(e.Value)
	s.remember(key, record{entry: entry, rev: e.Revision})
	return entry, e.Revision, true, nil
}

func (s *Sweeper) remember(key string, r record) {
	if s.records == nil {
		s.records = map[string]record{}
	}
	s.records[key] = r
}

// write stores the record over the revision it was read at, so a second
// writer that should not exist loses instead of interleaving; a lost write
// forgets what was held, and the bucket is asked again.
func (s *Sweeper) write(ctx context.Context, key string, e trailers.Entry, rev uint64, have bool) error {
	b, err := trailers.Encode(e)
	if err != nil {
		return err
	}
	var next uint64
	if have {
		next, err = s.KV.Update(ctx, key, b, rev)
	} else {
		next, err = s.KV.Create(ctx, key, b)
	}
	if err != nil {
		delete(s.records, key)
		return err
	}
	s.remember(key, record{entry: e, rev: next})
	return nil
}

// forget removes the record of every item that is gone, and its file when
// the file lies in a root folder's trailers directory.
func (s *Sweeper) forget(ctx context.Context, live map[string]bool, roots map[string]string) error {
	keys, err := s.KV.Keys(ctx)
	if err != nil {
		return fmt.Errorf("list the trailer records: %w", err)
	}
	log := logging.FromContext(ctx)
	for _, key := range keys {
		if live[key] {
			continue
		}
		entry, _, have, err := s.read(ctx, key)
		if err != nil || !have {
			continue
		}
		if entry.Path != "" && inTrailersDir(entry.Path, roots) {
			if err := os.Remove(entry.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
				log.WarnContext(ctx, "trailers: remove a gone item's trailer", "path", entry.Path, "err", err)
				continue
			}
		}
		if err := s.KV.Delete(ctx, key); err != nil {
			log.WarnContext(ctx, "trailers: remove a gone item's record", "key", key, "err", err)
			continue
		}
		delete(s.verified, key)
		delete(s.records, key)
		log.InfoContext(ctx, "trailers: a gone item's trailer was removed", "key", key, "path", entry.Path)
	}
	return nil
}

// inTrailersDir is whether path lies directly in the trailers directory of
// one of the root folders: the only place a record's file is removed from.
func inTrailersDir(path string, roots map[string]string) bool {
	dir := filepath.Dir(filepath.Clean(path))
	for _, root := range roots {
		if dir == trailers.Dir(root) {
			return true
		}
	}
	return false
}
