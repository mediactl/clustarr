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

// Package metron is a metadata.ComicProvider and metadata.IDResolver for
// Metron (https://metron.cloud/api), the comic database
// docs/research/metadata.md §2.5 calls the best comic crosswalk: every
// series and issue carries its ComicVine and Grand Comics Database ids.
//
// The API is Django REST Framework. The shapes are the server's own
// (Metron-Project/metron: api/v1_0/serializers/series.py's
// SeriesListSerializer and SeriesSerializer, comicsdb/filters/series.py's
// SeriesFilter with its name and cv_id filters, metron/settings.py's
// PageNumberPagination with PAGE_SIZE 100, knox tokens with
// AUTH_HEADER_PREFIX "Bearer", and the 20/minute + 5000/day throttles)
// and its reference client's (Metron-Project/mokkari: schemas/series.py,
// schemas/issue.py, and the /series/{id}/issue_list/ endpoint
// series_issues_list calls). Lists are {"count","next","previous",
// "results"}.
//
// A Comic CR's source is ComicVine or MangaDex (design §4.2), never
// Metron, so this client is reached with ComicVine ids: Volume takes
// ids[comicvine] as well as ids[metron], and Issues always reads its
// argument as a ComicVine volume id -- the id the gateway's lookupIssues
// hands every ComicProvider -- crosswalking it through the cv_id filter.
// A bare Metron id and a bare ComicVine id are both plain integers, so
// accepting both in Issues would mean guessing which one arrived;
// IssuesBySeries takes a Metron id by name instead.
package metron

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
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

// DefaultBaseURL is Metron's API root.
const DefaultBaseURL = "https://metron.cloud/api"

// DefaultRate and DefaultBurst are Metron's burst throttle, 20 requests a
// minute (metron/settings.py DEFAULT_THROTTLE_RATES "burst"). The
// 5000/day sustained throttle is spec.rateLimit.perDay's to model.
const (
	DefaultRate  rate.Limit = 20.0 / 60.0
	DefaultBurst int        = 2
)

// maxIssuePages bounds how many 100-issue pages one Issues call follows:
// 5,000 issues, past any real series, and a stop for a "next" link that
// never ends.
const maxIssuePages = 50

// Sentinel errors.
var (
	// ErrNoToken is returned by New without an API token.
	ErrNoToken = errors.New("metron: an api token is required")
	// ErrInvalidID is returned, before any request, for an id of the
	// wrong shape -- a ComicVine issue guid ("4000-…") where a volume
	// belongs, or a non-numeric Metron id.
	ErrInvalidID = errors.New("metron: invalid id")
	// ErrAmbiguous means more than one Metron series carries the same
	// ComicVine id. Picking one would be a guess.
	ErrAmbiguous = errors.New("metron: more than one series matches")
)

var numericPattern = regexp.MustCompile(`^[0-9]+$`)

// comicVineVolume normalizes a ComicVine volume id to its bare number and
// its canonical "4050-<num>" guid, accepting either form, exactly as
// pkg/metadata/clients/comicvine does.
func comicVineVolume(id string) (num, guid string, err error) {
	prefix, rest, found := strings.Cut(id, "-")
	if !found {
		prefix, rest = "4050", id
	}
	if prefix != "4050" || !numericPattern.MatchString(rest) {
		return "", "", fmt.Errorf("%w: comicvine volume %q", ErrInvalidID, id)
	}
	return rest, "4050-" + rest, nil
}

// Config configures a Client.
type Config struct {
	HTTPClient *http.Client
	BaseURL    string
	Limiter    *rate.Limiter
	UserAgent  string
	// Token is a Metron API token.
	Token string
}

// Client is a metadata.ComicProvider and metadata.IDResolver backed by
// Metron.
type Client struct {
	h       *httpjson.Client
	baseURL string
	auth    http.Header
}

// New builds a Client, refusing a Config without a token.
func New(cfg Config) (*Client, error) {
	tok := strings.TrimSpace(cfg.Token)
	if tok == "" {
		return nil, ErrNoToken
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{
		h:       &httpjson.Client{Provider: "metron", HTTP: cfg.HTTPClient, Limiter: cfg.Limiter, UserAgent: cfg.UserAgent},
		baseURL: base,
		auth:    http.Header{"Authorization": {"Bearer " + strings.TrimPrefix(tok, "Bearer ")}, "Accept": {"application/json"}},
	}, nil
}

// Name identifies this provider for logging and metrics.
func (c *Client) Name() string { return "metron" }

// Capabilities describes what this provider supports.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{LookupBy: []string{extid.KeyMetron, metadata.KeyComicVine}, Search: true}
}

type ref struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type listSeries struct {
	ID         int64  `json:"id"`
	Series     string `json:"series"`
	YearBegan  *int32 `json:"year_began"`
	IssueCount int32  `json:"issue_count"`
	Publisher  *ref   `json:"publisher"`
	CVID       *int64 `json:"cv_id"`
	GCDID      *int64 `json:"gcd_id"`
}

type page[T any] struct {
	Count   int     `json:"count"`
	Next    *string `json:"next"`
	Results []T     `json:"results"`
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return c.h.GetJSON(ctx, u, c.auth, out)
}

// SearchVolumes searches Metron's series by name (SeriesFilter's
// multi-word "name" filter), returning the first page of up to 100.
func (c *Client) SearchVolumes(ctx context.Context, q string) ([]metadata.SearchHit, error) {
	ctx, span := tracing.Start(ctx, "metadata.metron.SearchVolumes")
	defer span.End()

	var p page[listSeries]
	if err := c.get(ctx, "/series/", url.Values{"name": {q}}, &p); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	hits := make([]metadata.SearchHit, 0, len(p.Results))
	for _, s := range p.Results {
		hit := metadata.SearchHit{IDs: seriesIDs(s.ID, s.CVID, s.GCDID), Title: s.Series}
		if s.YearBegan != nil {
			hit.Year = *s.YearBegan
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

func seriesIDs(id int64, cv, gcd *int64) metadata.ExternalIDs {
	ids := metadata.ExternalIDs{extid.KeyMetron: strconv.FormatInt(id, 10)}
	if cv != nil && *cv > 0 {
		ids[metadata.KeyComicVine] = "4050-" + strconv.FormatInt(*cv, 10)
	}
	if gcd != nil && *gcd > 0 {
		ids[extid.KeyGCD] = strconv.FormatInt(*gcd, 10)
	}
	return ids
}

// seriesByComicVine returns the one Metron series carrying a ComicVine
// volume id: ErrNotFound for none, ErrAmbiguous for several.
func (c *Client) seriesByComicVine(ctx context.Context, cvID string) (*listSeries, error) {
	num, guid, err := comicVineVolume(cvID)
	if err != nil {
		return nil, err
	}
	var p page[listSeries]
	if err := c.get(ctx, "/series/", url.Values{"cv_id": {num}}, &p); err != nil {
		return nil, err
	}
	switch len(p.Results) {
	case 0:
		return nil, fmt.Errorf("metron: no series for comicvine %s: %w", guid, metadata.ErrNotFound)
	case 1:
		return &p.Results[0], nil
	default:
		return nil, fmt.Errorf("%w: %d series carry comicvine %s", ErrAmbiguous, len(p.Results), guid)
	}
}

type detailSeries struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	SortName   string   `json:"sort_name"`
	AltNames   []string `json:"alt_names"`
	Language   string   `json:"language"`
	Status     string   `json:"status"`
	Publisher  *ref     `json:"publisher"`
	YearBegan  *int32   `json:"year_began"`
	YearEnd    *int32   `json:"year_end"`
	Desc       string   `json:"desc"`
	IssueCount int32    `json:"issue_count"`
	Genres     []ref    `json:"genres"`
	CVID       *int64   `json:"cv_id"`
	GCDID      *int64   `json:"gcd_id"`
}

// Volume fetches a series by ids[extid.KeyMetron], else by
// ids[metadata.KeyComicVine] through the cv_id filter. Any other id is
// ErrUnsupported without a request.
func (c *Client) Volume(ctx context.Context, ids metadata.ExternalIDs) (*metadata.ComicVolume, error) {
	ctx, span := tracing.Start(ctx, "metadata.metron.Volume")
	defer span.End()

	id, err := c.resolveSeriesID(ctx, ids)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	var s detailSeries
	if err := c.get(ctx, "/series/"+strconv.FormatInt(id, 10)+"/", nil, &s); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	v := &metadata.ComicVolume{
		IDs:              seriesIDs(s.ID, s.CVID, s.GCDID),
		Title:            s.Name,
		SortTitle:        s.SortName,
		Description:      s.Desc,
		StartYear:        s.YearBegan,
		EndYear:          s.YearEnd,
		IssueCount:       s.IssueCount,
		Status:           strings.ToLower(s.Status),
		OriginalLanguage: s.Language,
		Provenance:       []metadata.Provenance{{Provider: "metron", FetchedAt: time.Now().UTC()}},
	}
	if s.Publisher != nil {
		v.Publisher = s.Publisher.Name
	}
	for _, n := range s.AltNames {
		v.AltTitles = append(v.AltTitles, metadata.AltTitle{Title: n})
	}
	for _, g := range s.Genres {
		v.Genres = append(v.Genres, g.Name)
	}
	return v, nil
}

func (c *Client) resolveSeriesID(ctx context.Context, ids metadata.ExternalIDs) (int64, error) {
	if m := ids[extid.KeyMetron]; m != "" {
		if !numericPattern.MatchString(m) {
			return 0, fmt.Errorf("%w: metron %q", ErrInvalidID, m)
		}
		return strconv.ParseInt(m, 10, 64)
	}
	if cv := ids[metadata.KeyComicVine]; cv != "" {
		s, err := c.seriesByComicVine(ctx, cv)
		if err != nil {
			return 0, err
		}
		return s.ID, nil
	}
	return 0, fmt.Errorf("metron: needs %q or %q in ExternalIDs: %w", extid.KeyMetron, metadata.KeyComicVine, metadata.ErrUnsupported)
}

type listIssue struct {
	ID        int64   `json:"id"`
	Number    string  `json:"number"`
	CoverDate string  `json:"cover_date"`
	StoreDate *string `json:"store_date"`
	Image     *string `json:"image"`
}

// Issues lists the issues of a ComicVine volume, as Metron holds them.
// volumeID is always a ComicVine volume id, "4050-<num>" or "<num>" -- see
// the package doc for why it is never read as a Metron id.
func (c *Client) Issues(ctx context.Context, volumeID string) ([]metadata.ComicIssue, error) {
	ctx, span := tracing.Start(ctx, "metadata.metron.Issues")
	defer span.End()

	s, err := c.seriesByComicVine(ctx, volumeID)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	issues, err := c.issuesBySeries(ctx, s.ID)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	return issues, nil
}

// IssuesBySeries lists a Metron series' issues by its Metron id.
func (c *Client) IssuesBySeries(ctx context.Context, metronSeriesID string) ([]metadata.ComicIssue, error) {
	ctx, span := tracing.Start(ctx, "metadata.metron.IssuesBySeries")
	defer span.End()

	if !numericPattern.MatchString(metronSeriesID) {
		err := fmt.Errorf("%w: metron %q", ErrInvalidID, metronSeriesID)
		tracing.RecordError(span, err)
		return nil, err
	}
	id, _ := strconv.ParseInt(metronSeriesID, 10, 64)
	issues, err := c.issuesBySeries(ctx, id)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	return issues, nil
}

// issuesBySeries follows the issue list's pages. A "next" link is read for
// its query string only and re-applied to this client's own base URL, so
// a response can never steer the token to another host.
func (c *Client) issuesBySeries(ctx context.Context, seriesID int64) ([]metadata.ComicIssue, error) {
	path := "/series/" + strconv.FormatInt(seriesID, 10) + "/issue_list/"
	var q url.Values
	var out []metadata.ComicIssue
	for pageNo := 0; ; pageNo++ {
		if pageNo == maxIssuePages {
			return nil, fmt.Errorf("metron: series %d: issue list still paging after %d pages", seriesID, maxIssuePages)
		}
		var p page[listIssue]
		if err := c.get(ctx, path, q, &p); err != nil {
			return nil, err
		}
		for _, is := range p.Results {
			issue := metadata.ComicIssue{
				IDs:       metadata.ExternalIDs{extid.KeyMetron: strconv.FormatInt(is.ID, 10)},
				Number:    is.Number,
				CoverDate: parseDate(is.CoverDate),
			}
			if is.StoreDate != nil {
				issue.StoreDate = parseDate(*is.StoreDate)
			}
			if is.Image != nil && *is.Image != "" {
				issue.Image = &metadata.Image{Type: metadata.ImageTypeThumb, URL: *is.Image}
			}
			out = append(out, issue)
		}
		if p.Next == nil || *p.Next == "" {
			return out, nil
		}
		next, err := url.Parse(*p.Next)
		if err != nil {
			return nil, fmt.Errorf("metron: series %d: next page link: %w: %w", seriesID, metadata.ErrDecode, err)
		}
		q = next.Query()
	}
}

// Resolve adds Metron's crosswalk for a comic: given ids[comicvine] it adds
// the Metron and GCD ids, and given ids[metron] it adds the ComicVine and
// GCD ids. Any kind but a comic is ErrUnsupported without a request.
func (c *Client) Resolve(ctx context.Context, kind commonv1.MediaKind, ids metadata.ExternalIDs) (metadata.ExternalIDs, error) {
	ctx, span := tracing.Start(ctx, "metadata.metron.Resolve")
	defer span.End()

	if kind != commonv1.MediaKindComic {
		return nil, fmt.Errorf("metron: no crosswalk for kind %q: %w", kind, metadata.ErrUnsupported)
	}
	if ids[extid.KeyMetron] == "" && ids[metadata.KeyComicVine] != "" {
		s, err := c.seriesByComicVine(ctx, ids[metadata.KeyComicVine])
		if err != nil {
			tracing.RecordError(span, err)
			return nil, err
		}
		return seriesIDs(s.ID, s.CVID, s.GCDID), nil
	}
	v, err := c.Volume(ctx, ids)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	return v.IDs, nil
}

// probeComicVineVolume is Batman (1940), ComicVine volume 796. The cv_id
// filter answers 200 with a (possibly empty) list whether or not Metron
// holds it, so the probe proves the token without depending on the
// catalogue.
const probeComicVineVolume = "796"

// Ping proves the token is accepted with one filtered series list.
func (c *Client) Ping(ctx context.Context) error {
	ctx, span := tracing.Start(ctx, "metadata.metron.Ping")
	defer span.End()
	var p page[listSeries]
	if err := c.get(ctx, "/series/", url.Values{"cv_id": {probeComicVineVolume}}, &p); err != nil {
		tracing.RecordError(span, err)
		return err
	}
	return nil
}

func parseDate(s string) *time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}

var (
	_ metadata.ComicProvider = (*Client)(nil)
	_ metadata.IDResolver    = (*Client)(nil)
)
