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

// Package animelists is a metadata.IDResolver over Fribb/anime-lists'
// anime-list-full.json, the daily AniDB <-> TVDB <-> TMDB <-> IMDb <->
// AniList <-> MAL <-> Kitsu crosswalk docs/research/metadata.md §2.7 and
// §4.1 name for the "animelists" provider. It is a dataset, not an API:
// the client downloads the whole file (7.5 MB on 2026-09-23), indexes it
// in memory, and re-downloads it -- conditionally, on its ETag -- once the
// refresh interval has passed.
//
// The file's shape is taken from the live dataset, not the research note,
// which predates a rename: each entry is {"type", "anidb_id",
// "anilist_id", "mal_id", "kitsu_id", "tvdb_id", "imdb_id" (a list),
// "themoviedb_id" ({"tv": n} or {"movie": [n...]}), "season" ({"tvdb",
// "tmdb"}), "episode_offset" ({"tvdb","tmdb"})}, every field optional.
// testdata/metadata/animelists holds 35 real entries.
//
// AniDB, AniList, MAL and Kitsu ids name one entry each. A TVDB series
// does not: AniDB (and so this list) files each season, OVA, special and
// movie of a series as its own anime, all under one tvdb_id -- Attack on
// Titan's series has eleven entries. Resolve reads a TVDB (or TMDB tv)
// id as the series' first-season TV entry: the one entry of type "TV"
// whose TVDB season is 1 or unset and whose episode offset is 0 or unset.
// When there is not exactly one such entry it adds nothing: a series'
// anidb/anilist/mal/kitsu ids are then the caller's to set, not this
// crosswalk's to guess. On the 2026-09-23 file that rule names one entry
// for 3,644 of the 4,378 TVDB series, refuses 12 as ambiguous (Gundam SEED
// and SEED Destiny both claim season 1 of TVDB 254931), and finds none for
// 722 whose entries are all OVAs, specials or movies.
package animelists

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/extid"
	"github.com/mediactl/clustarr/pkg/metadata/clients/httpjson"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// DefaultURL is the dataset's raw GitHub URL.
const DefaultURL = "https://raw.githubusercontent.com/Fribb/anime-lists/master/anime-list-full.json"

// DefaultRefresh is how long a downloaded dataset is served before the
// client checks for a new one. Fribb rebuilds the file daily.
const DefaultRefresh = 24 * time.Hour

// MaxDatasetBytes caps one download: eight times the file's size on
// 2026-09-23, and far below what would hurt the gateway.
const MaxDatasetBytes int64 = 64 << 20

// DefaultRate and DefaultBurst are chosen, not published: GitHub's raw
// host is fetched about once a day.
const (
	DefaultRate  rate.Limit = 1
	DefaultBurst int        = 1
)

// Sentinel errors.
var (
	// ErrAmbiguous means an id names more than one entry and nothing
	// singles one out.
	ErrAmbiguous = errors.New("animelists: more than one entry matches")
	// ErrConflict means the caller's ids and the dataset disagree -- an
	// entry found by one id carries a different value for another id the
	// caller also gave. Adding the dataset's ids on top would mix two
	// titles.
	ErrConflict = errors.New("animelists: ids disagree with the dataset")
	// ErrInvalidID is an id that is not the positive integer its key
	// promises.
	ErrInvalidID = errors.New("animelists: invalid id")
)

var imdbPattern = regexp.MustCompile(`^tt[0-9]{7,10}$`)

// Config configures a Client.
type Config struct {
	HTTPClient *http.Client
	// URL overrides DefaultURL.
	URL       string
	Limiter   *rate.Limiter
	UserAgent string
	// Refresh overrides DefaultRefresh.
	Refresh time.Duration
	// Now is the clock the refresh interval is measured on; nil means
	// time.Now.
	Now func() time.Time
}

// Client is a metadata.IDResolver backed by the Fribb anime-lists
// dataset.
type Client struct {
	h       *httpjson.Client
	url     string
	refresh time.Duration
	now     func() time.Time

	// sem serializes downloads without blocking readers of the current
	// dataset, and lets a waiting caller give up with its context.
	sem chan struct{}

	mu       sync.RWMutex
	ds       *dataset
	etag     string
	loadedAt time.Time
}

// New builds a Client. Nothing is downloaded until the first Resolve.
func New(cfg Config) *Client {
	u := cfg.URL
	if u == "" {
		u = DefaultURL
	}
	refresh := cfg.Refresh
	if refresh <= 0 {
		refresh = DefaultRefresh
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		h:       &httpjson.Client{Provider: "animelists", HTTP: cfg.HTTPClient, Limiter: cfg.Limiter, UserAgent: cfg.UserAgent, MaxBody: MaxDatasetBytes, Now: now},
		url:     u,
		refresh: refresh,
		now:     now,
		sem:     make(chan struct{}, 1),
	}
}

// Name identifies this provider for logging and metrics.
func (c *Client) Name() string { return "animelists" }

// Capabilities describes what this provider supports.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{LookupBy: []string{extid.KeyAniDB, metadata.KeyAniList, extid.KeyMAL, extid.KeyKitsu, metadata.KeyTVDB, metadata.KeyTMDB, metadata.KeyIMDb}}
}

// imdbIDs accepts the list the live file carries and the bare string an
// older revision of it did.
type imdbIDs []string

func (v *imdbIDs) UnmarshalJSON(b []byte) error {
	var list []string
	if err := json.Unmarshal(b, &list); err == nil {
		*v = list
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	if one != "" {
		*v = imdbIDs{one}
	}
	return nil
}

type entry struct {
	Type    string  `json:"type"`
	AniDB   *int64  `json:"anidb_id"`
	AniList *int64  `json:"anilist_id"`
	MAL     *int64  `json:"mal_id"`
	Kitsu   *int64  `json:"kitsu_id"`
	TVDB    *int64  `json:"tvdb_id"`
	IMDb    imdbIDs `json:"imdb_id"`
	TMDB    *struct {
		TV    *int64  `json:"tv"`
		Movie []int64 `json:"movie"`
	} `json:"themoviedb_id"`
	Season *struct {
		TVDB *int32 `json:"tvdb"`
		TMDB *int32 `json:"tmdb"`
	} `json:"season"`
	Offset *struct {
		TVDB *int32 `json:"tvdb"`
		TMDB *int32 `json:"tmdb"`
	} `json:"episode_offset"`
}

// dataset is the file, indexed. The unique indexes hold nil for an id
// that more than one entry carries.
type dataset struct {
	byAniDB, byAniList, byMAL, byKitsu map[int64]*entry
	byTVDB, byTMDBTV, byTMDBMovie      map[int64][]*entry
	byIMDb                             map[string][]*entry
}

func index(entries []entry) *dataset {
	ds := &dataset{
		byAniDB: map[int64]*entry{}, byAniList: map[int64]*entry{}, byMAL: map[int64]*entry{}, byKitsu: map[int64]*entry{},
		byTVDB: map[int64][]*entry{}, byTMDBTV: map[int64][]*entry{}, byTMDBMovie: map[int64][]*entry{},
		byIMDb: map[string][]*entry{},
	}
	unique := func(m map[int64]*entry, id *int64, e *entry) {
		if id == nil || *id <= 0 {
			return
		}
		if _, seen := m[*id]; seen {
			m[*id] = nil // shared: ambiguous from here on
			return
		}
		m[*id] = e
	}
	for i := range entries {
		e := &entries[i]
		unique(ds.byAniDB, e.AniDB, e)
		unique(ds.byAniList, e.AniList, e)
		unique(ds.byMAL, e.MAL, e)
		unique(ds.byKitsu, e.Kitsu, e)
		if e.TVDB != nil && *e.TVDB > 0 {
			ds.byTVDB[*e.TVDB] = append(ds.byTVDB[*e.TVDB], e)
		}
		if e.TMDB != nil {
			if e.TMDB.TV != nil && *e.TMDB.TV > 0 {
				ds.byTMDBTV[*e.TMDB.TV] = append(ds.byTMDBTV[*e.TMDB.TV], e)
			}
			for _, m := range e.TMDB.Movie {
				ds.byTMDBMovie[m] = append(ds.byTMDBMovie[m], e)
			}
		}
		for _, im := range e.IMDb {
			ds.byIMDb[im] = append(ds.byIMDb[im], e)
		}
	}
	return ds
}

// load returns the current dataset, downloading it when there is none or
// it is older than the refresh interval. A failed refresh keeps serving
// the dataset it already has; only a failed first download is an error.
func (c *Client) load(ctx context.Context) (*dataset, error) {
	c.mu.RLock()
	ds, fresh := c.ds, c.ds != nil && c.now().Sub(c.loadedAt) < c.refresh
	c.mu.RUnlock()
	if fresh {
		return ds, nil
	}

	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.sem }()

	c.mu.RLock()
	ds, fresh = c.ds, c.ds != nil && c.now().Sub(c.loadedAt) < c.refresh
	etag := c.etag
	c.mu.RUnlock()
	if fresh {
		return ds, nil // another caller refreshed it while this one waited
	}

	next, nextETag, err := c.download(ctx, etag, ds != nil)
	if err != nil {
		if ds != nil {
			logging.FromContext(ctx).WarnContext(ctx, "animelists: refresh failed; serving the dataset already loaded", "error", err)
			c.mu.Lock()
			c.loadedAt = c.now() // retry after another interval, not on every call
			c.mu.Unlock()
			return ds, nil
		}
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if next != nil {
		c.ds, c.etag = next, nextETag
	}
	c.loadedAt = c.now()
	return c.ds, nil
}

// download fetches the file, conditionally when haveOne. A 304 returns a
// nil dataset: keep the current one.
func (c *Client) download(ctx context.Context, etag string, haveOne bool) (*dataset, string, error) {
	ctx, span := tracing.Start(ctx, "metadata.animelists.download")
	defer span.End()

	h := http.Header{}
	if haveOne && etag != "" {
		h.Set("If-None-Match", etag)
	}
	resp, err := c.h.Do(ctx, httpjson.Request{URL: c.url, Header: h})
	if err != nil {
		tracing.RecordError(span, err)
		return nil, "", err
	}
	if resp.StatusCode == http.StatusNotModified && haveOne {
		return nil, etag, nil
	}
	if err := c.h.Check(resp, c.url); err != nil {
		tracing.RecordError(span, err)
		return nil, "", err
	}
	var entries []entry
	if err := c.h.Decode(resp.Body, &entries); err != nil {
		tracing.RecordError(span, err)
		return nil, "", err
	}
	logging.FromContext(ctx).InfoContext(ctx, "animelists: dataset loaded", "entries", len(entries))
	return index(entries), resp.Header.Get("ETag"), nil
}

// Resolve crosswalks an anime series or movie through the dataset. Any
// other kind, or ids with nothing the dataset is keyed by, is
// ErrUnsupported without a download.
//
// For a series, an anidb, anilist, mal or kitsu id names its entry
// directly; failing those, a tvdb (else tmdb tv) id names the series'
// first-season TV entry, per the package doc. For a movie, those four
// names it directly; failing those, a tmdb or imdb id names the one MOVIE
// entry carrying it. The result is that entry's ids: anidb, anilist, mal
// and kitsu, the series' tvdb and tmdb tv ids (a series) or its tmdb movie
// id (a movie, when it lists exactly one), and its imdb id when it lists
// exactly one.
func (c *Client) Resolve(ctx context.Context, kind commonv1.MediaKind, ids metadata.ExternalIDs) (metadata.ExternalIDs, error) {
	ctx, span := tracing.Start(ctx, "metadata.animelists.Resolve")
	defer span.End()

	if kind != commonv1.MediaKindSeries && kind != commonv1.MediaKindMovie {
		return nil, fmt.Errorf("animelists: no crosswalk for kind %q: %w", kind, metadata.ErrUnsupported)
	}
	if !hasAny(ids, extid.KeyAniDB, metadata.KeyAniList, extid.KeyMAL, extid.KeyKitsu, metadata.KeyTVDB, metadata.KeyTMDB, metadata.KeyIMDb) {
		return nil, fmt.Errorf("animelists: needs an anidb, anilist, mal, kitsu, tvdb, tmdb or imdb id: %w", metadata.ErrUnsupported)
	}
	ds, err := c.load(ctx)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	e, err := find(ds, kind, ids)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	out := idsOf(e, kind)
	for k, v := range out {
		if have, ok := ids[k]; ok && have != v && (k == metadata.KeyTVDB || k == metadata.KeyTMDB) {
			err := fmt.Errorf("%w: the entry found has %s %s, the caller gave %s", ErrConflict, k, v, have)
			tracing.RecordError(span, err)
			return nil, err
		}
	}
	return out, nil
}

func find(ds *dataset, kind commonv1.MediaKind, ids metadata.ExternalIDs) (*entry, error) {
	for _, u := range []struct {
		key string
		idx map[int64]*entry
	}{{extid.KeyAniDB, ds.byAniDB}, {metadata.KeyAniList, ds.byAniList}, {extid.KeyMAL, ds.byMAL}, {extid.KeyKitsu, ds.byKitsu}} {
		v := ids[u.key]
		if v == "" {
			continue
		}
		n, err := positive(u.key, v)
		if err != nil {
			return nil, err
		}
		e, ok := u.idx[n]
		switch {
		case !ok:
			return nil, fmt.Errorf("animelists: no entry with %s %d: %w", u.key, n, metadata.ErrNotFound)
		case e == nil:
			return nil, fmt.Errorf("%w: %s %d", ErrAmbiguous, u.key, n)
		default:
			return e, nil
		}
	}

	if kind == commonv1.MediaKindSeries {
		for _, s := range []struct {
			key    string
			idx    map[int64][]*entry
			season func(*entry) *int32
			offset func(*entry) *int32
		}{
			{metadata.KeyTVDB, ds.byTVDB, seasonTVDB, offsetTVDB},
			{metadata.KeyTMDB, ds.byTMDBTV, seasonTMDB, offsetTMDB},
		} {
			v := ids[s.key]
			if v == "" {
				continue
			}
			n, err := positive(s.key, v)
			if err != nil {
				return nil, err
			}
			all := s.idx[n]
			if len(all) == 0 {
				return nil, fmt.Errorf("animelists: no entry with %s %d: %w", s.key, n, metadata.ErrNotFound)
			}
			first := slices.DeleteFunc(slices.Clone(all), func(e *entry) bool {
				season, offset := s.season(e), s.offset(e)
				return e.Type != "TV" || (season != nil && *season != 1) || (offset != nil && *offset != 0)
			})
			if len(first) != 1 {
				return nil, fmt.Errorf("%w: %s %d has %d entries and %d first-season TV entries", ErrAmbiguous, s.key, n, len(all), len(first))
			}
			return first[0], nil
		}
		return nil, fmt.Errorf("animelists: a series needs an anidb, anilist, mal, kitsu, tvdb or tmdb id: %w", metadata.ErrUnsupported)
	}

	var candidates []*entry
	switch {
	case ids[metadata.KeyTMDB] != "":
		n, err := positive(metadata.KeyTMDB, ids[metadata.KeyTMDB])
		if err != nil {
			return nil, err
		}
		candidates = ds.byTMDBMovie[n]
	case ids[metadata.KeyIMDb] != "":
		if !imdbPattern.MatchString(ids[metadata.KeyIMDb]) {
			return nil, fmt.Errorf("%w: imdb %q", ErrInvalidID, ids[metadata.KeyIMDb])
		}
		candidates = ds.byIMDb[ids[metadata.KeyIMDb]]
	default:
		return nil, fmt.Errorf("animelists: a movie needs an anidb, anilist, mal, kitsu, tmdb or imdb id: %w", metadata.ErrUnsupported)
	}
	movies := slices.DeleteFunc(slices.Clone(candidates), func(e *entry) bool { return e.Type != "MOVIE" })
	switch len(movies) {
	case 0:
		return nil, fmt.Errorf("animelists: no movie entry for %v: %w", ids, metadata.ErrNotFound)
	case 1:
		return movies[0], nil
	default:
		return nil, fmt.Errorf("%w: %d movie entries for %v", ErrAmbiguous, len(movies), ids)
	}
}

func idsOf(e *entry, kind commonv1.MediaKind) metadata.ExternalIDs {
	out := metadata.ExternalIDs{}
	put := func(key string, v *int64) {
		if v != nil && *v > 0 {
			out[key] = strconv.FormatInt(*v, 10)
		}
	}
	put(extid.KeyAniDB, e.AniDB)
	put(metadata.KeyAniList, e.AniList)
	put(extid.KeyMAL, e.MAL)
	put(extid.KeyKitsu, e.Kitsu)
	if kind == commonv1.MediaKindSeries {
		put(metadata.KeyTVDB, e.TVDB)
		if e.TMDB != nil {
			put(metadata.KeyTMDB, e.TMDB.TV)
		}
	}
	if kind == commonv1.MediaKindMovie && e.TMDB != nil && len(e.TMDB.Movie) == 1 {
		put(metadata.KeyTMDB, &e.TMDB.Movie[0])
	}
	if len(e.IMDb) == 1 && imdbPattern.MatchString(e.IMDb[0]) {
		out[metadata.KeyIMDb] = e.IMDb[0]
	}
	return out
}

// Ping proves the dataset is reachable with a HEAD request, without
// downloading it.
func (c *Client) Ping(ctx context.Context) error {
	ctx, span := tracing.Start(ctx, "metadata.animelists.Ping")
	defer span.End()
	resp, err := c.h.Do(ctx, httpjson.Request{Method: http.MethodHead, URL: c.url})
	if err == nil {
		err = c.h.Check(resp, c.url)
	}
	if err != nil {
		tracing.RecordError(span, err)
	}
	return err
}

func seasonTVDB(e *entry) *int32 {
	if e.Season == nil {
		return nil
	}
	return e.Season.TVDB
}

func seasonTMDB(e *entry) *int32 {
	if e.Season == nil {
		return nil
	}
	return e.Season.TMDB
}

func offsetTVDB(e *entry) *int32 {
	if e.Offset == nil {
		return nil
	}
	return e.Offset.TVDB
}

func offsetTMDB(e *entry) *int32 {
	if e.Offset == nil {
		return nil
	}
	return e.Offset.TMDB
}

func hasAny(ids metadata.ExternalIDs, keys ...string) bool {
	for _, k := range keys {
		if ids[k] != "" {
			return true
		}
	}
	return false
}

func positive(key, v string) (int64, error) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: %s %q", ErrInvalidID, key, v)
	}
	return n, nil
}

var _ metadata.IDResolver = (*Client)(nil)
