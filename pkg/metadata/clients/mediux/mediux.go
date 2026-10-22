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
	"slices"
	"strings"
	"time"

	"golang.org/x/time/rate"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/imageset"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/httpjson"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// DefaultBaseURL is the host AURA posts to, and the one the shared token
// is good for.
const DefaultBaseURL = "https://images.mediux.io"

// ErrNoToken is New without a token.
var ErrNoToken = errors.New("mediux: a token is required")

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
	SeasonPosters    []SeasonPoster
	Titlecards       []Titlecard
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
)

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
	return out.Movie, nil
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
		return fmt.Errorf("mediux: graphql: %s (%s)", e.Message, e.Extensions.Code)
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

// Overrides is a show set as an item's overrides: the poster and backdrop
// as spec.artwork entries, the season posters and titlecards as
// spec.seasonArtwork entries, in season then episode order.
func (c *Client) Overrides(s *ShowSet) imageset.Entry {
	e := imageset.Entry{ID: s.ID}
	if len(s.Poster) > 0 {
		e.Artwork = append(e.Artwork, catalogv1.ArtworkOverride{Type: catalogv1.ImageTypePoster, URL: c.ImageURL(s.Poster[0])})
	}
	if len(s.Backdrop) > 0 {
		e.Artwork = append(e.Artwork, catalogv1.ArtworkOverride{Type: catalogv1.ImageTypeFanart, URL: c.ImageURL(s.Backdrop[0])})
	}
	posters := slices.Clone(s.SeasonPosters)
	slices.SortStableFunc(posters, func(a, b SeasonPoster) int { return int(a.Season - b.Season) })
	seen := map[int32]bool{}
	for _, p := range posters {
		if seen[p.Season] {
			continue
		}
		seen[p.Season] = true
		e.SeasonArtwork = append(e.SeasonArtwork, catalogv1.SeasonArtworkOverride{Season: p.Season, Type: catalogv1.ImageTypePoster, URL: c.ImageURL(p.File)})
	}
	cards := slices.Clone(s.Titlecards)
	slices.SortStableFunc(cards, func(a, b Titlecard) int {
		if a.Season != b.Season {
			return int(a.Season - b.Season)
		}
		return int(a.Episode - b.Episode)
	})
	seenEp := map[[2]int32]bool{}
	for _, t := range cards {
		k := [2]int32{t.Season, t.Episode}
		if t.Episode < 1 || seenEp[k] {
			continue
		}
		seenEp[k] = true
		n := t.Episode
		e.SeasonArtwork = append(e.SeasonArtwork, catalogv1.SeasonArtworkOverride{Season: t.Season, Episode: &n, Type: catalogv1.ImageTypeScreenshot, URL: c.ImageURL(t.File)})
	}
	return e
}

// MovieOverrides is a film set as the film's spec.artwork overrides.
func (c *Client) MovieOverrides(s *MovieSet) imageset.Entry {
	e := imageset.Entry{ID: s.ID}
	if len(s.Poster) > 0 {
		e.Artwork = append(e.Artwork, catalogv1.ArtworkOverride{Type: catalogv1.ImageTypePoster, URL: c.ImageURL(s.Poster[0])})
	}
	if len(s.Backdrop) > 0 {
		e.Artwork = append(e.Artwork, catalogv1.ArtworkOverride{Type: catalogv1.ImageTypeFanart, URL: c.ImageURL(s.Backdrop[0])})
	}
	return e
}

// Policy is how Choose ranks a show's sets.
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

// Choice is Choose's answer.
type Choice struct {
	Set    *ShowSet
	Reason string
}

// Choose ranks a show's sets: a preferred creator's first, then one whose
// poster is in a preferred language, then the more complete (season
// posters, then titlecards, against the series), then popularity, then the
// most recently updated. A set with no poster is never chosen. Nil when
// nothing qualifies.
func Choose(sets []ShowSet, p Policy) *Choice {
	var best *ShowSet
	var bestScore [6]int
	var bestReason string
	for i := range sets {
		s := &sets[i]
		if len(s.Poster) == 0 {
			continue
		}
		score, reason := p.score(s)
		if best == nil || compare(score, bestScore) > 0 {
			best, bestScore, bestReason = s, score, reason
		}
	}
	if best == nil {
		return nil
	}
	return &Choice{Set: best, Reason: bestReason}
}

func (p Policy) score(s *ShowSet) ([6]int, string) {
	var sc [6]int
	var why []string
	if i := slices.IndexFunc(p.PreferredCreators, func(u string) bool { return strings.EqualFold(u, s.Creator.Username) }); i >= 0 {
		sc[0] = len(p.PreferredCreators) - i
		why = append(why, "preferred creator "+s.Creator.Username)
	}
	if len(p.Languages) > 0 && len(s.Poster) > 0 && slices.ContainsFunc(p.Languages, func(l string) bool { return strings.EqualFold(l, s.Poster[0].Language) }) {
		sc[1] = 1
		why = append(why, "poster in "+s.Poster[0].Language)
	}
	seasons := map[int32]bool{}
	for _, sp := range s.SeasonPosters {
		seasons[sp.Season] = true
	}
	covered := 0
	for _, n := range p.Seasons {
		if seasons[n] {
			covered++
		}
	}
	sc[2] = covered
	cards := map[int32]int32{}
	seenEp := map[[2]int32]bool{}
	for _, t := range s.Titlecards {
		k := [2]int32{t.Season, t.Episode}
		if seenEp[k] {
			continue
		}
		seenEp[k] = true
		cards[t.Season]++
	}
	total := 0
	for n, want := range p.Episodes {
		total += int(min(cards[n], want))
	}
	sc[3] = total
	if s.Popularity != nil {
		sc[4] = *s.Popularity
	}
	sc[5] = int(s.Updated.Unix() / 60)
	why = append(why, fmt.Sprintf("%d of %d season posters, %d titlecards, popularity %d", covered, len(p.Seasons), total, sc[4]))
	return sc, strings.Join(why, "; ")
}

func compare(a, b [6]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] > b[i] {
				return 1
			}
			return -1
		}
	}
	return 0
}
