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

// Package anilist is a metadata.IDResolver and metadata.ComicProvider for
// AniList (https://graphql.anilist.co), a public GraphQL API over anime
// and manga (docs/research/metadata.md §2.5, §2.7).
//
// Its role in Clustarr is the AniList <-> MyAnimeList crosswalk for anime
// series and movies and for manga -- design §4.2 lists both ids among
// SeriesMetadata's and ComicMetadata's ExternalIDs -- and manga metadata
// for a comic whose ids already carry an AniList or MAL id (MangaDex
// publishes both in its links). AniList has no TVDB or ComicVine ids, so
// it serves nothing keyed by those alone; that is kitsu's and animelists'
// crosswalk to make.
//
// Reads need no auth. AniList documents 90 requests a minute but has run
// degraded at 30 for years (research note; the live X-RateLimit-Limit
// header read 30 on 2026-09-23), and answers an unknown id with HTTP 404
// and {"errors":[{"message":"Not Found.","status":404}],"data":{"Media":
// null}}. The fixtures under testdata/metadata/anilist are live responses
// to exactly the queries this file sends.
package anilist

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/extid"
	"github.com/mediactl/clustarr/pkg/metadata/clients/httpjson"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// DefaultBaseURL is AniList's GraphQL endpoint.
const DefaultBaseURL = "https://graphql.anilist.co"

// DefaultRate and DefaultBurst are AniList's current (degraded) limit of 30
// requests a minute.
const (
	DefaultRate  rate.Limit = 30.0 / 60.0
	DefaultBurst int        = 2
)

// Sentinel errors.
var (
	// ErrInvalidID is returned, before any request, for an AniList or MAL
	// id that is not a positive integer.
	ErrInvalidID = errors.New("anilist: invalid id")
	// ErrQuery is a GraphQL-level failure reported with HTTP 200.
	ErrQuery = errors.New("anilist: query failed")
)

// Config configures a Client.
type Config struct {
	HTTPClient *http.Client
	BaseURL    string
	Limiter    *rate.Limiter
	UserAgent  string
}

// Client is a metadata.IDResolver and metadata.ComicProvider backed by
// AniList.
type Client struct {
	h        *httpjson.Client
	endpoint string
}

// New builds a Client.
func New(cfg Config) *Client {
	endpoint := cfg.BaseURL
	if endpoint == "" {
		endpoint = DefaultBaseURL
	}
	return &Client{h: &httpjson.Client{Provider: "anilist", HTTP: cfg.HTTPClient, Limiter: cfg.Limiter, UserAgent: cfg.UserAgent}, endpoint: endpoint}
}

// Name identifies this provider for logging and metrics.
func (c *Client) Name() string { return "anilist" }

// Capabilities describes what this provider supports.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{LookupBy: []string{metadata.KeyAniList, extid.KeyMAL}, Search: true}
}

func (c *Client) query(ctx context.Context, q string, vars map[string]any, out any) error {
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := c.h.PostJSON(ctx, c.endpoint, nil, map[string]any{"query": q, "variables": vars}, &env); err != nil {
		return err
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("%w: %s", ErrQuery, strings.Join(msgs, "; "))
	}
	return c.h.Decode(env.Data, out)
}

const mediaFields = `id idMal type format status(version: 2) description(asHtml: false)
  startDate { year month day } endDate { year month day }
  volumes chapters episodes countryOfOrigin isAdult genres synonyms averageScore popularity
  title { romaji english native } coverImage { extraLarge large } bannerImage`

const (
	mediaQuery  = `query Media($id: Int, $idMal: Int, $type: MediaType) { Media(id: $id, idMal: $idMal, type: $type) { ` + mediaFields + ` } }`
	idsQuery    = `query IDs($id: Int, $idMal: Int, $type: MediaType) { Media(id: $id, idMal: $idMal, type: $type) { id idMal type } }`
	searchQuery = `query Search($search: String!, $type: MediaType) { Page(page: 1, perPage: 25) { media(search: $search, type: $type) { id idMal title { romaji english native } startDate { year } coverImage { large } } } }`
)

type fuzzyDate struct {
	Year  *int32 `json:"year"`
	Month *int   `json:"month"`
	Day   *int   `json:"day"`
}

type title struct {
	Romaji  string `json:"romaji"`
	English string `json:"english"`
	Native  string `json:"native"`
}

type media struct {
	ID              int64     `json:"id"`
	IDMal           *int64    `json:"idMal"`
	Type            string    `json:"type"`
	Format          string    `json:"format"`
	Status          string    `json:"status"`
	Description     string    `json:"description"`
	StartDate       fuzzyDate `json:"startDate"`
	EndDate         fuzzyDate `json:"endDate"`
	Volumes         *int32    `json:"volumes"`
	CountryOfOrigin string    `json:"countryOfOrigin"`
	IsAdult         bool      `json:"isAdult"`
	Genres          []string  `json:"genres"`
	Synonyms        []string  `json:"synonyms"`
	AverageScore    *int32    `json:"averageScore"`
	Popularity      int32     `json:"popularity"`
	Title           title     `json:"title"`
	CoverImage      struct {
		ExtraLarge string `json:"extraLarge"`
		Large      string `json:"large"`
	} `json:"coverImage"`
	BannerImage string `json:"bannerImage"`
}

func (m *media) ids() metadata.ExternalIDs {
	ids := metadata.ExternalIDs{metadata.KeyAniList: strconv.FormatInt(m.ID, 10)}
	if m.IDMal != nil && *m.IDMal > 0 {
		ids[extid.KeyMAL] = strconv.FormatInt(*m.IDMal, 10)
	}
	return ids
}

// mediaType is AniList's MediaType for a Clustarr kind: anime for a series
// or a movie, manga for a comic, and nothing for any other kind.
func mediaType(kind commonv1.MediaKind) (string, bool) {
	switch kind {
	case commonv1.MediaKindSeries, commonv1.MediaKindMovie:
		return "ANIME", true
	case commonv1.MediaKindComic:
		return "MANGA", true
	default:
		return "", false
	}
}

// lookupVars builds the id variables for a Media query from ids: the
// AniList id when present, else the MAL id with the type -- MAL numbers
// anime and manga separately, so a MAL id means nothing without it.
func lookupVars(ids metadata.ExternalIDs, typ string) (map[string]any, error) {
	if v := ids[metadata.KeyAniList]; v != "" {
		n, err := positive(v)
		if err != nil {
			return nil, err
		}
		return map[string]any{"id": n, "type": typ}, nil
	}
	if v := ids[extid.KeyMAL]; v != "" {
		n, err := positive(v)
		if err != nil {
			return nil, err
		}
		return map[string]any{"idMal": n, "type": typ}, nil
	}
	return nil, fmt.Errorf("anilist: needs %q or %q in ExternalIDs: %w", metadata.KeyAniList, extid.KeyMAL, metadata.ErrUnsupported)
}

// Resolve adds the AniList and MAL ids of the anime (for a series or
// movie) or manga (for a comic) that ids names by either. Any other kind,
// or ids with neither, is ErrUnsupported without a request.
func (c *Client) Resolve(ctx context.Context, kind commonv1.MediaKind, ids metadata.ExternalIDs) (metadata.ExternalIDs, error) {
	ctx, span := tracing.Start(ctx, "metadata.anilist.Resolve")
	defer span.End()

	typ, ok := mediaType(kind)
	if !ok {
		return nil, fmt.Errorf("anilist: no crosswalk for kind %q: %w", kind, metadata.ErrUnsupported)
	}
	vars, err := lookupVars(ids, typ)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	var out struct {
		Media *media `json:"Media"`
	}
	if err := c.query(ctx, idsQuery, vars, &out); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if out.Media == nil {
		return nil, fmt.Errorf("anilist: %s %v: %w", typ, vars, metadata.ErrNotFound)
	}
	return out.Media.ids(), nil
}

// SearchVolumes searches AniList's manga by title, up to 25 hits.
func (c *Client) SearchVolumes(ctx context.Context, q string) ([]metadata.SearchHit, error) {
	ctx, span := tracing.Start(ctx, "metadata.anilist.SearchVolumes")
	defer span.End()

	var out struct {
		Page struct {
			Media []media `json:"media"`
		} `json:"Page"`
	}
	if err := c.query(ctx, searchQuery, map[string]any{"search": q, "type": "MANGA"}, &out); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	hits := make([]metadata.SearchHit, 0, len(out.Page.Media))
	for i := range out.Page.Media {
		m := &out.Page.Media[i]
		hit := metadata.SearchHit{IDs: m.ids(), Title: displayTitle(m.Title), Poster: m.CoverImage.Large}
		if m.StartDate.Year != nil {
			hit.Year = *m.StartDate.Year
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

// Volume fetches a manga by ids[anilist] or ids[mal]. Any other id is
// ErrUnsupported without a request.
func (c *Client) Volume(ctx context.Context, ids metadata.ExternalIDs) (*metadata.ComicVolume, error) {
	ctx, span := tracing.Start(ctx, "metadata.anilist.Volume")
	defer span.End()

	vars, err := lookupVars(ids, "MANGA")
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	var out struct {
		Media *media `json:"Media"`
	}
	if err := c.query(ctx, mediaQuery, vars, &out); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if out.Media == nil {
		return nil, fmt.Errorf("anilist: manga %v: %w", vars, metadata.ErrNotFound)
	}
	m := out.Media
	v := &metadata.ComicVolume{
		IDs:              m.ids(),
		Kind:             "manga",
		Title:            displayTitle(m.Title),
		Description:      m.Description,
		StartYear:        m.StartDate.Year,
		EndYear:          m.EndDate.Year,
		Status:           status(m.Status),
		OriginalLanguage: originalLanguage(m.CountryOfOrigin),
		Genres:           m.Genres,
		Provenance:       []metadata.Provenance{{Provider: "anilist", FetchedAt: time.Now().UTC()}},
	}
	if m.Volumes != nil {
		v.IssueCount = *m.Volumes
	}
	if m.IsAdult {
		v.AgeRating = "adult"
	}
	for _, t := range []struct{ title, lang string }{{m.Title.Romaji, "ja-ro"}, {m.Title.English, "en"}, {m.Title.Native, originalLanguage(m.CountryOfOrigin)}} {
		if t.title != "" && t.title != v.Title {
			v.AltTitles = append(v.AltTitles, metadata.AltTitle{Title: t.title, Language: t.lang})
		}
	}
	for _, s := range m.Synonyms {
		if s != "" && !slices.ContainsFunc(v.AltTitles, func(a metadata.AltTitle) bool { return a.Title == s }) {
			v.AltTitles = append(v.AltTitles, metadata.AltTitle{Title: s})
		}
	}
	if u := cmp.Or(m.CoverImage.ExtraLarge, m.CoverImage.Large); u != "" {
		v.Images = append(v.Images, metadata.Image{Type: metadata.ImageTypePoster, URL: u})
	}
	if m.BannerImage != "" {
		v.Images = append(v.Images, metadata.Image{Type: metadata.ImageTypeBanner, URL: m.BannerImage})
	}
	if m.AverageScore != nil {
		// averageScore is 0-100; metadata.Rating is /10 in hundredths.
		v.Ratings = metadata.Ratings{"anilist": {Source: "anilist", ValueCentis: int32(math.Round(float64(*m.AverageScore) * 10))}}
	}
	return v, nil
}

// Issues is ErrUnsupported: AniList knows how many volumes a manga has
// but nothing about any one of them, and inventing issues 1..N from a
// count is not metadata.
func (c *Client) Issues(ctx context.Context, volumeID string) ([]metadata.ComicIssue, error) {
	return nil, fmt.Errorf("anilist: no per-volume data for %q: %w", volumeID, metadata.ErrUnsupported)
}

// probeAniListID is Cowboy Bebop, AniList anime 1 -- AniList's first entry.
const probeAniListID = 1

// Ping looks up one anime's ids.
func (c *Client) Ping(ctx context.Context) error {
	ctx, span := tracing.Start(ctx, "metadata.anilist.Ping")
	defer span.End()
	var out struct {
		Media *media `json:"Media"`
	}
	err := c.query(ctx, idsQuery, map[string]any{"id": probeAniListID, "type": "ANIME"}, &out)
	if err != nil && !errors.Is(err, metadata.ErrNotFound) {
		tracing.RecordError(span, err)
		return err
	}
	return nil
}

// displayTitle prefers the English title, then the romanization AniList
// files every title under.
func displayTitle(t title) string {
	return cmp.Or(t.English, t.Romaji, t.Native)
}

// status maps AniList's MediaStatus onto the lower-case words the other
// comic clients use.
func status(s string) string {
	switch s {
	case "FINISHED":
		return "completed"
	case "RELEASING":
		return "ongoing"
	case "HIATUS":
		return "hiatus"
	case "CANCELLED":
		return "cancelled"
	case "NOT_YET_RELEASED":
		return "upcoming"
	default:
		return strings.ToLower(s)
	}
}

// originalLanguage maps AniList's countryOfOrigin (ISO 3166-1: JP, KR,
// CN, TW) onto the language manga from there is written in; any other
// country maps to nothing rather than a guess.
func originalLanguage(country string) string {
	switch country {
	case "JP":
		return "ja"
	case "KR":
		return "ko"
	case "CN", "TW":
		return "zh"
	default:
		return ""
	}
}

func positive(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: %q", ErrInvalidID, s)
	}
	return n, nil
}

var (
	_ metadata.IDResolver    = (*Client)(nil)
	_ metadata.ComicProvider = (*Client)(nil)
)
