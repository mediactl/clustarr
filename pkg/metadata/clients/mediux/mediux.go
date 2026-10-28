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

// Package mediux is a client for MediUX's image sets
// (docs/superpowers/specs/2026-10-22-mediux-image-sets-automation-design.md):
// a Directus GraphQL endpoint, as the MediUX team's AURA and Mediux-Posters
// use it, recorded live on 2026-10-22 with the shared testing token
// (test/data/metadata/mediux).
//
//   - The endpoint is <BaseURL>/graphql with a personal bearer token;
//     https://images.mediux.io takes the shared token, https://api.mediux.pro
//     answers "Invalid user credentials." to it.
//   - A show's or film's id is its TMDB id; shows carry tvdb_id, and
//     shows(filter: {tvdb_id}) maps a TheTVDB id to one.
//   - A set's images are Directus files: id, modified_on and src, which is
//     the file's versioned path ("/<id>?v=20240112193259"). The image is
//     <BaseURL>/assets<src>, anonymously, with &key=jpg for a compressed
//     JPEG (378 KB for a 509 KB WebP original), &key=thumb for an AVIF
//     thumbnail.
//   - content_ids lists every TMDB id that has a set (17,134 on
//     2026-10-22), so a library can be matched without a query per item.
//
// The caller owns the rate limiter (Config.Limiter), as every client; no
// limit is published, so the provider defaults to a conservative one.
package mediux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/httpjson"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// DefaultBaseURL is the host AURA posts to, and the one the shared token
// is good for.
const DefaultBaseURL = "https://images.mediux.io"

// ErrNoToken is New without a token.
var ErrNoToken = errors.New("mediux: a token is required")

// DefaultRate and DefaultBurst are the limit a caller should give this
// client with no spec.rateLimit: MediUX publishes none, so half a request a
// second (30 a minute) with a burst of five is the conservative choice, and
// the cache in front of it asks for an item once a week at most.
const (
	DefaultRate  rate.Limit = 0.5
	DefaultBurst int        = 5
)

// Quality is which rendering of an image a set's URLs name.
type Quality string

const (
	// QualityOptimized is AURA's "optimized": a compressed JPEG (key=jpg).
	QualityOptimized Quality = "optimized"
	// QualityOriginal is the file the creator uploaded.
	QualityOriginal Quality = "original"
)

// Config configures a Client.
type Config struct {
	HTTPClient *http.Client
	// BaseURL is the API's host; DefaultBaseURL when empty.
	BaseURL   string
	Limiter   *rate.Limiter
	UserAgent string
	// Token is the personal bearer token.
	Token string
	// Quality is the rendering a set's URLs name; optimized when empty.
	Quality Quality
}

// Client asks MediUX for sets.
type Client struct {
	h       *httpjson.Client
	baseURL string
	token   string
	quality Quality
}

// New makes a Client; a token is required.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, ErrNoToken
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	q := cfg.Quality
	if q == "" {
		q = QualityOptimized
	}
	return &Client{
		h:       &httpjson.Client{Provider: "mediux", HTTP: cfg.HTTPClient, Limiter: cfg.Limiter, UserAgent: cfg.UserAgent, MaxBody: 16 << 20},
		baseURL: base, token: strings.TrimSpace(cfg.Token), quality: q,
	}, nil
}

// Name is the provider's name.
func (c *Client) Name() string { return "mediux" }

// File is one Directus file of a set.
type File struct {
	ID         string    `json:"id"`
	ModifiedOn time.Time `json:"modified_on"`
	// Src is the file's versioned path, "/<id>?v=<stamp>".
	Src      string `json:"src"`
	Language string `json:"-"`
}

// UnmarshalJSON reads the language's display name into Language.
func (f *File) UnmarshalJSON(b []byte) error {
	type raw struct {
		ID         string    `json:"id"`
		ModifiedOn time.Time `json:"modified_on"`
		Src        string    `json:"src"`
		Language   *struct {
			DisplayName string `json:"display_name"`
		} `json:"language"`
	}
	var r raw
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	f.ID, f.ModifiedOn, f.Src = r.ID, r.ModifiedOn, r.Src
	if r.Language != nil {
		f.Language = r.Language.DisplayName
	}
	return nil
}

// MarshalJSON writes the wire shape UnmarshalJSON reads, so a File cached
// as JSON comes back whole (the language as {display_name}).
func (f File) MarshalJSON() ([]byte, error) {
	return json.Marshal(f.raw())
}

func (f File) raw() rawFile {
	r := rawFile{ID: f.ID, ModifiedOn: f.ModifiedOn, Src: f.Src}
	if f.Language != "" {
		r.Language = &struct {
			DisplayName string `json:"display_name"`
		}{DisplayName: f.Language}
	}
	return r
}

// Creator is a set's maker.
type Creator struct {
	Username string `json:"username"`
}

// ShowSet is one show's set.
type ShowSet struct {
	ID               string    `json:"id"`
	Title            string    `json:"set_title"`
	Creator          Creator   `json:"user_created"`
	Created          time.Time `json:"date_created"`
	Updated          time.Time `json:"date_updated"`
	Popularity       *int      `json:"popularity"`
	PopularityGlobal *int      `json:"popularity_global"`
	Poster           []File    `json:"show_poster"`
	Backdrop         []File    `json:"show_backdrop"`
	// SeasonPosters and Titlecards are read and written by the set's own
	// (Un)MarshalJSON, in the wire's nested shape.
	SeasonPosters []SeasonPoster `json:"-"`
	Titlecards    []Titlecard    `json:"-"`
}

// SeasonPoster is a season's poster in a set.
type SeasonPoster struct {
	File
	Season int32
}

// Titlecard is an episode's thumbnail in a set.
type Titlecard struct {
	File
	Season  int32
	Episode int32
}

// rawFile is a File as the wire has it, without File's own UnmarshalJSON,
// so an embedding struct is decoded field by field (a promoted
// UnmarshalJSON would decode the whole object as a File).
type rawFile struct {
	ID         string    `json:"id"`
	ModifiedOn time.Time `json:"modified_on"`
	Src        string    `json:"src"`
	Language   *struct {
		DisplayName string `json:"display_name"`
	} `json:"language"`
}

func (r rawFile) file() File {
	f := File{ID: r.ID, ModifiedOn: r.ModifiedOn, Src: r.Src}
	if r.Language != nil {
		f.Language = r.Language.DisplayName
	}
	return f
}

func (s *ShowSet) UnmarshalJSON(b []byte) error {
	type alias ShowSet
	var r struct {
		alias
		SeasonPosters []struct {
			rawFile
			Season struct {
				Number int32 `json:"season_number"`
			} `json:"season"`
		} `json:"season_posters"`
		Titlecards []struct {
			rawFile
			Episode struct {
				Number int32 `json:"episode_number"`
				Season struct {
					Number int32 `json:"season_number"`
				} `json:"season_id"`
			} `json:"episode"`
		} `json:"titlecards"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	*s = ShowSet(r.alias)
	for _, p := range r.SeasonPosters {
		s.SeasonPosters = append(s.SeasonPosters, SeasonPoster{File: p.file(), Season: p.Season.Number})
	}
	for _, t := range r.Titlecards {
		s.Titlecards = append(s.Titlecards, Titlecard{File: t.file(), Season: t.Episode.Season.Number, Episode: t.Episode.Number})
	}
	return nil
}

// MarshalJSON writes the wire shape UnmarshalJSON reads -- season posters
// under season_posters with their season, titlecards under titlecards with
// their episode and season -- so a set cached as JSON (mediuxcache) comes
// back with its seasons and episodes, which the choice's completeness
// terms score. Before this, a cached set round-tripped as poster-only and
// the second choice for a series was never the first's (2026-10-22).
func (s ShowSet) MarshalJSON() ([]byte, error) {
	type alias ShowSet
	type seasonRef struct {
		Number int32 `json:"season_number"`
	}
	type poster struct {
		rawFile
		Season seasonRef `json:"season"`
	}
	type card struct {
		rawFile
		Episode struct {
			Number int32     `json:"episode_number"`
			Season seasonRef `json:"season_id"`
		} `json:"episode"`
	}
	out := struct {
		alias
		SeasonPosters []poster `json:"season_posters"`
		Titlecards    []card   `json:"titlecards"`
	}{alias: alias(s)}
	out.alias.SeasonPosters, out.alias.Titlecards = nil, nil
	for _, p := range s.SeasonPosters {
		out.SeasonPosters = append(out.SeasonPosters, poster{rawFile: p.raw(), Season: seasonRef{Number: p.Season}})
	}
	for _, t := range s.Titlecards {
		c := card{rawFile: t.raw()}
		c.Episode.Number, c.Episode.Season = t.Episode, seasonRef{Number: t.Season}
		out.Titlecards = append(out.Titlecards, c)
	}
	return json.Marshal(out)
}

// Show is a show as MediUX records it, with its sets.
type Show struct {
	ID     string    `json:"id"` // the TMDB id
	Title  string    `json:"title"`
	TVDBID string    `json:"tvdb_id"`
	IMDBID string    `json:"imdb_id"`
	Sets   []ShowSet `json:"show_sets"`
}

// MovieSet is one film's set.
type MovieSet struct {
	ID               string    `json:"id"`
	Title            string    `json:"set_title"`
	Creator          Creator   `json:"user_created"`
	Created          time.Time `json:"date_created"`
	Updated          time.Time `json:"date_updated"`
	Popularity       *int      `json:"popularity"`
	PopularityGlobal *int      `json:"popularity_global"`
	Poster           []File    `json:"movie_poster"`
	Backdrop         []File    `json:"movie_backdrop"`
}

// Movie is a film as MediUX records it, with its sets.
type Movie struct {
	ID     string     `json:"id"` // the TMDB id
	Title  string     `json:"title"`
	TVDBID string     `json:"tvdb_id"`
	IMDBID string     `json:"imdb_id"`
	Sets   []MovieSet `json:"movie_sets"`
	// CollectionSets are the collection sets' images of this film, one
	// entry per collection set (its id, title, creator and rank, the
	// film's poster and backdrop in it), from MovieSets' second query.
	// The key is the cache's own, not the wire's.
	CollectionSets []MovieSet `json:"collectionSets,omitempty"`
}

// CollectionSet is one collection's set.
type CollectionSet struct {
	ID         string    `json:"id"`
	Title      string    `json:"set_title"`
	Creator    Creator   `json:"user_created"`
	Updated    time.Time `json:"date_updated"`
	Popularity *int      `json:"popularity"`
	Poster     []File    `json:"collection_poster"`
	Backdrop   []File    `json:"collection_backdrop"`
}

// Collection is a TMDB collection as MediUX records it, with its sets.
type Collection struct {
	ID   string          `json:"id"`
	Name string          `json:"collection_name"`
	Sets []CollectionSet `json:"collection_sets"`
}

// ContentIDs is every TMDB id MediUX has a set for, by kind.
type ContentIDs struct {
	Movies map[string]bool
	Shows  map[string]bool
}

const (
	queryShowByTVDB = `query($tvdb: String!) { shows(filter: { tvdb_id: { _eq: $tvdb } }) { id tvdb_id title } }`
	fileFields      = `{ id modified_on src language { display_name } }`
	showSetFields   = `id set_title user_created { username } date_created date_updated popularity popularity_global ` +
		`show_poster ` + fileFields + ` show_backdrop ` + fileFields + ` ` +
		`season_posters(filter: { season: { season_number: { _nnull: true } } }) { id modified_on src language { display_name } season { season_number } } ` +
		`titlecards(filter: { episode: { episode_number: { _nnull: true } season_id: { season_number: { _nnull: true } } } }) { id modified_on src language { display_name } episode { episode_number season_id { season_number } } }`
	queryShowSets = `query($tmdb: ID!) { shows_by_id(id: $tmdb) { id title tvdb_id imdb_id ` +
		`show_sets(filter: { _or: [ { show_poster: { id: { _nnull: true } } } { show_backdrop: { id: { _nnull: true } } } { season_posters: { id: { _nnull: true } } } { titlecards: { id: { _nnull: true } } } ] }) { ` + showSetFields + ` } } }`
	queryShowSet   = `query($id: ID!) { show_sets_by_id(id: $id) { ` + showSetFields + ` } }`
	movieSetFields = `id set_title user_created { username } date_created date_updated popularity popularity_global movie_poster ` + fileFields + ` movie_backdrop ` + fileFields
	queryMovieSets = `query($tmdb: ID!) { movies_by_id(id: $tmdb) { id title tvdb_id imdb_id movie_sets { ` + movieSetFields + ` } } }`
	queryMovieSet  = `query($id: ID!) { movie_sets_by_id(id: $id) { ` + movieSetFields + ` } }`
	queryCollSets  = `query($id: ID!) { collections_by_id(id: $id) { id collection_name collection_sets { id set_title user_created { username } date_updated popularity collection_poster ` + fileFields + ` collection_backdrop ` + fileFields + ` } } }`
	queryContent   = `{ content_ids(limit: -1) { id type } }`
	// queryFilmCollectionImages is a film's images in collection sets,
	// AURA's shape (a film's posters and backdrops with collection_set);
	// not recorded live, so MovieSets survives a schema error from it.
	collectionSetRef          = `collection_set { id set_title user_created { username } date_created date_updated popularity }`
	queryFilmCollectionImages = `query($tmdb: ID!) { movies_by_id(id: $tmdb) { id ` +
		`posters(filter: { collection_set: { id: { _nnull: true } } }) { id modified_on src language { display_name } ` + collectionSetRef + ` } ` +
		`backdrops(filter: { collection_set: { id: { _nnull: true } } }) { id modified_on src language { display_name } ` + collectionSetRef + ` } } }`
)

// ErrQuery is a GraphQL error MediUX answered that is neither an auth
// failure nor a limit: a query it cannot run as written.
var ErrQuery = errors.New("mediux: query refused")

// ShowByTVDB is the TMDB id of the show TheTVDB numbers tvdbID, or
// ErrNotFound.
func (c *Client) ShowByTVDB(ctx context.Context, tvdbID string) (string, error) {
	ctx, span := tracing.Start(ctx, "metadata.mediux.ShowByTVDB")
	defer span.End()
	var out struct {
		Shows []struct {
			ID string `json:"id"`
		} `json:"shows"`
	}
	if err := c.query(ctx, queryShowByTVDB, map[string]any{"tvdb": tvdbID}, &out); err != nil {
		tracing.RecordError(span, err)
		return "", err
	}
	if len(out.Shows) == 0 || out.Shows[0].ID == "" {
		return "", fmt.Errorf("mediux: no show for tvdb %s: %w", tvdbID, metadata.ErrNotFound)
	}
	return out.Shows[0].ID, nil
}

// ShowSets is the show tmdbID with every set that has at least one image,
// or ErrNotFound for a show MediUX does not have.
func (c *Client) ShowSets(ctx context.Context, tmdbID string) (*Show, error) {
	ctx, span := tracing.Start(ctx, "metadata.mediux.ShowSets")
	defer span.End()
	var out struct {
		Show *Show `json:"shows_by_id"`
	}
	if err := c.query(ctx, queryShowSets, map[string]any{"tmdb": tmdbID}, &out); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if out.Show == nil || out.Show.ID == "" {
		return nil, fmt.Errorf("mediux: no show %s: %w", tmdbID, metadata.ErrNotFound)
	}
	return out.Show, nil
}

// ShowSet is one set by id, or ErrNotFound.
func (c *Client) ShowSet(ctx context.Context, id string) (*ShowSet, error) {
	ctx, span := tracing.Start(ctx, "metadata.mediux.ShowSet")
	defer span.End()
	var out struct {
		Set *ShowSet `json:"show_sets_by_id"`
	}
	if err := c.query(ctx, queryShowSet, map[string]any{"id": id}, &out); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if out.Set == nil || out.Set.ID == "" {
		return nil, fmt.Errorf("mediux: no set %s: %w", id, metadata.ErrNotFound)
	}
	return out.Set, nil
}

// MovieSets is the film tmdbID with its sets, or ErrNotFound.
func (c *Client) MovieSets(ctx context.Context, tmdbID string) (*Movie, error) {
	ctx, span := tracing.Start(ctx, "metadata.mediux.MovieSets")
	defer span.End()
	var out struct {
		Movie *Movie `json:"movies_by_id"`
	}
	if err := c.query(ctx, queryMovieSets, map[string]any{"tmdb": tmdbID}, &out); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if out.Movie == nil || out.Movie.ID == "" {
		return nil, fmt.Errorf("mediux: no movie %s: %w", tmdbID, metadata.ErrNotFound)
	}
	sets, err := c.filmCollectionSets(ctx, tmdbID)
	switch {
	case errors.Is(err, ErrQuery):
		// The film keeps its own sets; the second query was never
		// recorded live, and a schema MediUX changed must not cost them.
		logging.FromContext(ctx).WarnContext(ctx, "mediux: a film's collection-set images could not be asked for; using its movie sets alone", "tmdb", tmdbID, "error", err)
	case err != nil:
		tracing.RecordError(span, err)
		return nil, err
	}
	out.Movie.CollectionSets = sets
	return out.Movie, nil
}

// filmCollectionSets is the collection sets' images of the film tmdbID,
// grouped by set in the order MediUX lists them.
func (c *Client) filmCollectionSets(ctx context.Context, tmdbID string) ([]MovieSet, error) {
	type file struct {
		rawFile
		Set *struct {
			ID         string    `json:"id"`
			Title      string    `json:"set_title"`
			Creator    Creator   `json:"user_created"`
			Created    time.Time `json:"date_created"`
			Updated    time.Time `json:"date_updated"`
			Popularity *int      `json:"popularity"`
		} `json:"collection_set"`
	}
	var out struct {
		Movie *struct {
			Posters   []file `json:"posters"`
			Backdrops []file `json:"backdrops"`
		} `json:"movies_by_id"`
	}
	if err := c.query(ctx, queryFilmCollectionImages, map[string]any{"tmdb": tmdbID}, &out); err != nil {
		return nil, err
	}
	if out.Movie == nil {
		return nil, nil
	}
	var sets []MovieSet
	at := map[string]int{}
	add := func(f file, backdrop bool) {
		if f.Set == nil || f.Set.ID == "" {
			return
		}
		i, ok := at[f.Set.ID]
		if !ok {
			i = len(sets)
			at[f.Set.ID] = i
			sets = append(sets, MovieSet{ID: f.Set.ID, Title: f.Set.Title, Creator: f.Set.Creator, Created: f.Set.Created, Updated: f.Set.Updated, Popularity: f.Set.Popularity})
		}
		if backdrop {
			sets[i].Backdrop = append(sets[i].Backdrop, f.file())
		} else {
			sets[i].Poster = append(sets[i].Poster, f.file())
		}
	}
	for _, f := range out.Movie.Posters {
		add(f, false)
	}
	for _, f := range out.Movie.Backdrops {
		add(f, true)
	}
	return sets, nil
}

// MovieSet is one film set by id, or ErrNotFound.
func (c *Client) MovieSet(ctx context.Context, id string) (*MovieSet, error) {
	ctx, span := tracing.Start(ctx, "metadata.mediux.MovieSet")
	defer span.End()
	var out struct {
		Set *MovieSet `json:"movie_sets_by_id"`
	}
	if err := c.query(ctx, queryMovieSet, map[string]any{"id": id}, &out); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if out.Set == nil || out.Set.ID == "" {
		return nil, fmt.Errorf("mediux: no movie set %s: %w", id, metadata.ErrNotFound)
	}
	return out.Set, nil
}

// CollectionSets is the TMDB collection tmdbID with its sets, or ErrNotFound.
func (c *Client) CollectionSets(ctx context.Context, tmdbID string) (*Collection, error) {
	ctx, span := tracing.Start(ctx, "metadata.mediux.CollectionSets")
	defer span.End()
	var out struct {
		Collection *Collection `json:"collections_by_id"`
	}
	if err := c.query(ctx, queryCollSets, map[string]any{"id": tmdbID}, &out); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if out.Collection == nil || out.Collection.ID == "" {
		return nil, fmt.Errorf("mediux: no collection %s: %w", tmdbID, metadata.ErrNotFound)
	}
	return out.Collection, nil
}

// ContentIDs is every TMDB id MediUX has a set for: one request that
// answers "is there anything for this item?" for a whole library.
func (c *Client) ContentIDs(ctx context.Context) (*ContentIDs, error) {
	ctx, span := tracing.Start(ctx, "metadata.mediux.ContentIDs")
	defer span.End()
	var out struct {
		IDs []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"content_ids"`
	}
	if err := c.query(ctx, queryContent, nil, &out); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	ids := &ContentIDs{Movies: map[string]bool{}, Shows: map[string]bool{}}
	for _, e := range out.IDs {
		switch e.Type {
		case "movie":
			ids.Movies[e.ID] = true
		case "show":
			ids.Shows[e.ID] = true
		}
	}
	return ids, nil
}

// query posts one GraphQL query and decodes its data. A GraphQL error
// naming invalid credentials is ErrAuth; any other is reported as it came.
func (c *Client) query(ctx context.Context, q string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": q, "variables": vars})
	if err != nil {
		return fmt.Errorf("mediux: encode query: %w", err)
	}
	h := http.Header{"Authorization": {"Bearer " + c.token}, "Content-Type": {"application/json"}}
	resp, err := c.h.Do(ctx, httpjson.Request{Method: http.MethodPost, URL: c.baseURL + "/graphql", Header: h, Body: body})
	if err != nil {
		return err
	}
	if err := c.h.Check(resp, c.baseURL+"/graphql"); err != nil {
		return err
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message    string `json:"message"`
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if err := c.h.Decode(resp.Body, &envelope); err != nil {
		return err
	}
	if len(envelope.Errors) > 0 {
		e := envelope.Errors[0]
		if e.Extensions.Code == "INVALID_CREDENTIALS" || e.Extensions.Code == "TOKEN_EXPIRED" || e.Extensions.Code == "FORBIDDEN" {
			return fmt.Errorf("mediux: %s: %w", e.Message, metadata.ErrAuth)
		}
		return fmt.Errorf("%w: %s (%s)", ErrQuery, e.Message, e.Extensions.Code)
	}
	if len(envelope.Data) == 0 {
		return fmt.Errorf("mediux: %w: no data", metadata.ErrDecode)
	}
	return c.h.Decode(envelope.Data, out)
}

// ImageURL is the URL a set's file is fetched from at the client's
// quality: <BaseURL>/assets<src>, plus &key=jpg for optimized.
func (c *Client) ImageURL(f File) string {
	return ImageURL(c.baseURL, f, c.quality)
}

// ImageURL is Client.ImageURL for a base URL and quality.
func ImageURL(baseURL string, f File, q Quality) string {
	src := f.Src
	if src == "" {
		src = "/" + f.ID + "?v=" + f.ModifiedOn.UTC().Format("20060102150405")
	}
	u := strings.TrimRight(baseURL, "/") + "/assets" + src
	if q != QualityOriginal {
		if strings.Contains(u, "?") {
			u += "&key=jpg"
		} else {
			u += "?key=jpg"
		}
	}
	return u
}

// Policy is how a composition ranks an item's sets.
type Policy struct {
	// PreferredCreators are usernames whose sets win, earlier first.
	PreferredCreators []string
	// Languages are image languages to prefer, as MediUX displays them
	// ("English"); a set whose poster is in one outranks one whose is not.
	Languages []string
	// Seasons are the series' season numbers; a set with a poster for each
	// is complete.
	Seasons []int32
	// Episodes is, by season, how many episodes the series has; a set with
	// a titlecard for each is complete.
	Episodes map[int32]int32
}

// SetURL is a set's page on MediUX, as AURA links it:
// https://mediux.io/<kind>-set/<id>.
func SetURL(kind SetKind, id string) string {
	return "https://mediux.io/" + string(kind) + "-set/" + id
}

// Ping proves the token is accepted: the smallest query the API answers
// only to a bearer it knows.
func (c *Client) Ping(ctx context.Context) error {
	ctx, span := tracing.Start(ctx, "metadata.mediux.Ping")
	defer span.End()
	var out struct {
		IDs []struct {
			ID string `json:"id"`
		} `json:"content_ids"`
	}
	if err := c.query(ctx, queryPing, nil, &out); err != nil {
		tracing.RecordError(span, err)
		return err
	}
	return nil
}

// Capabilities implements metadata.Provider: the client looks items up by
// TMDB id (and a show by its TheTVDB id) and supplies artwork.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{LookupBy: []string{metadata.KeyTMDB, metadata.KeyTVDB}, Artwork: true}
}

const queryPing = `{ content_ids(limit: 1) { id } }`
