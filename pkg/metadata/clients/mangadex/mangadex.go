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

// Package mangadex is a metadata.ComicProvider and metadata.IDResolver for
// MangaDex (https://api.mangadex.org), the manga source a Comic CR names
// with source "mangadex" and a manga UUID (design §4.2).
//
// Reads need no auth, but every request must carry a User-Agent, and the
// service allows about five requests a second per IP
// (docs/research/metadata.md §2.5). The shapes are the live API's,
// captured on 2026-09-23 into testdata/metadata/mangadex (trimmed, not
// edited): GET /manga?title= and GET /manga/{id} answer
// {"result":"ok","response":...,"data":...} with attributes {title,
// altTitles, description, links, originalLanguage, lastVolume, status,
// year, contentRating, tags[{attributes{name, group}}]} and, with
// includes[]=cover_art, a cover_art relationship whose fileName is served
// at https://uploads.mangadex.org/covers/{manga}/{fileName};
// GET /manga/{id}/aggregate answers {"result":"ok","volumes":{"<n>":
// {"volume","count","chapters"}}}; an unknown manga is a 404 with
// {"result":"error","errors":[...]}.
//
// A MangaDex "issue" is a tankōbon volume, not a chapter: a chapter upload
// is one scanlation group's translation, while releases of a series are
// collected and named by volume, which is what ComicSpec's
// "volumeAsIssue" special version expects. Chapters not yet collected into
// a volume (MangaDex's "none" bucket) are no issue yet.
package mangadex

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/extid"
	"github.com/mediactl/clustarr/pkg/metadata/clients/internal/httpjson"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// DefaultBaseURL is MangaDex's API root.
const DefaultBaseURL = "https://api.mangadex.org"

// DefaultCoverBaseURL is where cover files are served.
const DefaultCoverBaseURL = "https://uploads.mangadex.org"

// DefaultRate and DefaultBurst are MangaDex's documented global limit,
// about five requests a second per IP (docs/research/metadata.md §2.5).
const (
	DefaultRate  rate.Limit = 5
	DefaultBurst int        = 5
)

// ErrInvalidID is returned, before any request, for a manga id that is not
// a UUID -- a ComicVine id handed over by the gateway's lookupIssues
// included, which must never be read as a manga.
var ErrInvalidID = errors.New("mangadex: not a manga UUID")

var (
	uuidPattern    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	numericPattern = regexp.MustCompile(`^[0-9]+$`)
)

// Config configures a Client.
type Config struct {
	HTTPClient *http.Client
	BaseURL    string
	// CoverBaseURL overrides DefaultCoverBaseURL.
	CoverBaseURL string
	Limiter      *rate.Limiter
	UserAgent    string
}

// Client is a metadata.ComicProvider and metadata.IDResolver backed by
// MangaDex.
type Client struct {
	h         *httpjson.Client
	baseURL   string
	coverBase string
}

// New builds a Client.
func New(cfg Config) *Client {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	cover := strings.TrimRight(cfg.CoverBaseURL, "/")
	if cover == "" {
		cover = DefaultCoverBaseURL
	}
	return &Client{
		h:         &httpjson.Client{Provider: "mangadex", HTTP: cfg.HTTPClient, Limiter: cfg.Limiter, UserAgent: cfg.UserAgent},
		baseURL:   base,
		coverBase: cover,
	}
}

// Name identifies this provider for logging and metrics.
func (c *Client) Name() string { return "mangadex" }

// Capabilities describes what this provider supports.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{LookupBy: []string{extid.KeyMangaDex}, Search: true}
}

type localized map[string]string

type manga struct {
	ID         string `json:"id"`
	Attributes struct {
		Title            localized   `json:"title"`
		AltTitles        []localized `json:"altTitles"`
		Description      localized   `json:"description"`
		Links            localized   `json:"links"`
		OriginalLanguage string      `json:"originalLanguage"`
		LastVolume       *string     `json:"lastVolume"`
		Status           string      `json:"status"`
		Year             *int32      `json:"year"`
		ContentRating    string      `json:"contentRating"`
		Tags             []struct {
			Attributes struct {
				Name  localized `json:"name"`
				Group string    `json:"group"`
			} `json:"attributes"`
		} `json:"tags"`
	} `json:"attributes"`
	Relationships []struct {
		Type       string `json:"type"`
		Attributes *struct {
			FileName string `json:"fileName"`
		} `json:"attributes"`
	} `json:"relationships"`
}

// SearchVolumes searches MangaDex by title, most relevant first, up to 25
// hits.
func (c *Client) SearchVolumes(ctx context.Context, q string) ([]metadata.SearchHit, error) {
	ctx, span := tracing.Start(ctx, "metadata.mangadex.SearchVolumes")
	defer span.End()

	// contentRating[] is MangaDex's own default, spelled out so a change of
	// default upstream does not silently change what a search can find.
	params := url.Values{
		"title":            {q},
		"limit":            {"25"},
		"includes[]":       {"cover_art"},
		"order[relevance]": {"desc"},
		"contentRating[]":  {"safe", "suggestive", "erotica"},
	}
	var out struct {
		Data []manga `json:"data"`
	}
	if err := c.h.GetJSON(ctx, c.baseURL+"/manga?"+params.Encode(), nil, &out); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	hits := make([]metadata.SearchHit, 0, len(out.Data))
	for i := range out.Data {
		m := &out.Data[i]
		hit := metadata.SearchHit{IDs: c.ids(m), Title: preferredTitle(m.Attributes.Title), Poster: c.coverURL(m)}
		if m.Attributes.Year != nil {
			hit.Year = *m.Attributes.Year
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

// Volume fetches a manga by ids[extid.KeyMangaDex]. Any other id is
// ErrUnsupported without a request.
func (c *Client) Volume(ctx context.Context, ids metadata.ExternalIDs) (*metadata.ComicVolume, error) {
	ctx, span := tracing.Start(ctx, "metadata.mangadex.Volume")
	defer span.End()

	m, err := c.manga(ctx, ids)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	a := &m.Attributes
	v := &metadata.ComicVolume{
		IDs:              c.ids(m),
		Kind:             "manga",
		Title:            preferredTitle(a.Title),
		Description:      a.Description["en"],
		StartYear:        a.Year,
		Status:           a.Status,
		AgeRating:        a.ContentRating,
		OriginalLanguage: a.OriginalLanguage,
		Provenance:       []metadata.Provenance{{Provider: "mangadex", FetchedAt: time.Now().UTC()}},
	}
	if a.LastVolume != nil {
		if n, err := strconv.ParseInt(*a.LastVolume, 10, 32); err == nil && n > 0 {
			// Set only once MangaDex knows the final volume -- a completed
			// series; an ongoing one leaves lastVolume empty.
			v.IssueCount = int32(n)
		}
	}
	for _, alt := range a.AltTitles {
		for _, lang := range sortedKeys(alt) {
			v.AltTitles = append(v.AltTitles, metadata.AltTitle{Title: alt[lang], Language: lang})
		}
	}
	for _, t := range a.Tags {
		name := t.Attributes.Name["en"]
		if name == "" {
			continue
		}
		if t.Attributes.Group == "genre" {
			v.Genres = append(v.Genres, name)
		} else {
			v.Tags = append(v.Tags, name)
		}
	}
	if u := c.coverURL(m); u != "" {
		v.Images = []metadata.Image{{Type: metadata.ImageTypePoster, URL: u}}
	}
	return v, nil
}

func (c *Client) manga(ctx context.Context, ids metadata.ExternalIDs) (*manga, error) {
	id := ids[extid.KeyMangaDex]
	if id == "" {
		return nil, fmt.Errorf("mangadex: needs %q in ExternalIDs: %w", extid.KeyMangaDex, metadata.ErrUnsupported)
	}
	if !uuidPattern.MatchString(id) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	var out struct {
		Data manga `json:"data"`
	}
	if err := c.h.GetJSON(ctx, c.baseURL+"/manga/"+id+"?includes[]=cover_art", nil, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

type aggVolume struct {
	Volume string `json:"volume"`
	Count  int    `json:"count"`
}

// Issues lists a manga's collected volumes, in volume order, from
// MangaDex's aggregate. volumeID must be a manga UUID; the gateway's
// lookupIssues hands every ComicProvider a ComicVine id, which is refused
// here -- as ErrUnsupported as well as ErrInvalidID -- rather than read as
// a manga.
func (c *Client) Issues(ctx context.Context, volumeID string) ([]metadata.ComicIssue, error) {
	ctx, span := tracing.Start(ctx, "metadata.mangadex.Issues")
	defer span.End()

	if !uuidPattern.MatchString(volumeID) {
		err := fmt.Errorf("%w: %q: %w", ErrInvalidID, volumeID, metadata.ErrUnsupported)
		tracing.RecordError(span, err)
		return nil, err
	}
	var out struct {
		Volumes json.RawMessage `json:"volumes"`
	}
	if err := c.h.GetJSON(ctx, c.baseURL+"/manga/"+volumeID+"/aggregate", nil, &out); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	vols, err := decodeVolumes(out.Volumes)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	issues := make([]metadata.ComicIssue, 0, len(vols))
	for _, v := range vols {
		if v.Volume == "" || v.Volume == "none" {
			continue
		}
		issues = append(issues, metadata.ComicIssue{Number: v.Volume})
	}
	slices.SortStableFunc(issues, func(a, b metadata.ComicIssue) int { return compareNumbers(a.Number, b.Number) })
	return issues, nil
}

// decodeVolumes reads the aggregate's volumes, which MangaDex serializes as
// an object keyed by volume -- or, when there are none, as an empty array.
func decodeVolumes(raw json.RawMessage) ([]aggVolume, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var byKey map[string]aggVolume
	if err := json.Unmarshal(raw, &byKey); err == nil {
		out := make([]aggVolume, 0, len(byKey))
		for k, v := range byKey {
			if v.Volume == "" {
				v.Volume = k
			}
			out = append(out, v)
		}
		return out, nil
	}
	var list []aggVolume
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("mangadex: aggregate volumes: %w: %w", metadata.ErrDecode, err)
	}
	return list, nil
}

// compareNumbers orders volume numbers numerically ("2" before "10"),
// with any that are not numbers after them in string order.
func compareNumbers(a, b string) int {
	fa, errA := strconv.ParseFloat(a, 64)
	fb, errB := strconv.ParseFloat(b, 64)
	switch {
	case errA == nil && errB == nil:
		return cmp.Compare(fa, fb)
	case errA == nil:
		return -1
	case errB == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

// Resolve adds the crosswalk MangaDex publishes in a manga's links --
// AniList ("al"), MyAnimeList ("mal"), Kitsu ("kt") and MangaUpdates
// ("mu") -- for a comic keyed by ids[extid.KeyMangaDex]. Any other kind is
// ErrUnsupported without a request.
func (c *Client) Resolve(ctx context.Context, kind commonv1.MediaKind, ids metadata.ExternalIDs) (metadata.ExternalIDs, error) {
	ctx, span := tracing.Start(ctx, "metadata.mangadex.Resolve")
	defer span.End()

	if kind != commonv1.MediaKindComic {
		return nil, fmt.Errorf("mangadex: no crosswalk for kind %q: %w", kind, metadata.ErrUnsupported)
	}
	m, err := c.manga(ctx, ids)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	return c.ids(m), nil
}

// Ping runs one single-result title search.
func (c *Client) Ping(ctx context.Context) error {
	ctx, span := tracing.Start(ctx, "metadata.mangadex.Ping")
	defer span.End()
	var out struct {
		Result string `json:"result"`
	}
	if err := c.h.GetJSON(ctx, c.baseURL+"/manga?limit=1&title=berserk", nil, &out); err != nil {
		tracing.RecordError(span, err)
		return err
	}
	return nil
}

// ids is a manga's own id plus the numeric crosswalk ids its links carry.
// A link that is not the shape its key promises is left out, not passed
// on.
func (c *Client) ids(m *manga) metadata.ExternalIDs {
	ids := metadata.ExternalIDs{extid.KeyMangaDex: m.ID}
	links := m.Attributes.Links
	for link, key := range map[string]string{"al": metadata.KeyAniList, "mal": extid.KeyMAL, "kt": extid.KeyKitsu} {
		if v := links[link]; numericPattern.MatchString(v) {
			ids[key] = v
		}
	}
	if v := links["mu"]; v != "" && !strings.ContainsAny(v, "/:?#") {
		ids[extid.KeyMangaUpdates] = v
	}
	return ids
}

func (c *Client) coverURL(m *manga) string {
	for _, r := range m.Relationships {
		if r.Type == "cover_art" && r.Attributes != nil && r.Attributes.FileName != "" {
			return c.coverBase + "/covers/" + m.ID + "/" + r.Attributes.FileName
		}
	}
	return ""
}

// preferredTitle picks a manga's display title: English, else the
// romanized Japanese MangaDex usually files a title under, else the first
// language in key order -- map order is random, and a title must not be.
func preferredTitle(t localized) string {
	for _, lang := range []string{"en", "ja-ro"} {
		if v := t[lang]; v != "" {
			return v
		}
	}
	for _, k := range sortedKeys(t) {
		if t[k] != "" {
			return t[k]
		}
	}
	return ""
}

func sortedKeys(m localized) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

var (
	_ metadata.ComicProvider = (*Client)(nil)
	_ metadata.IDResolver    = (*Client)(nil)
)
