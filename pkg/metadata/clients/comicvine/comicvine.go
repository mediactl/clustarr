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

// Package comicvine is an in-house metadata.ComicProvider for ComicVine
// (https://comicvine.gamespot.com/api), the primary comic/manga metadata
// source (docs/research/metadata.md §2.5). There is no adopted Go client
// module, so every request goes through net/http directly.
//
// Every ComicVine response is wrapped in a status envelope
// ({"error", "status_code", "results", ...}), and the envelope's
// status_code -- not the HTTP status -- is ComicVine's own verdict. doGet
// maps both; see mapStatusCode for the table and its sources.
//
// The API key travels in the query string, so no error this client returns
// carries a request URL: net/http's *url.Error is unwrapped to its cause,
// and paths are logged without their query.
package comicvine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

const defaultBaseURL = "https://comicvine.gamespot.com/api"

// resourceTypeVolume is ComicVine's numeric resource-type prefix for a
// volume guid (docs/research/metadata.md §2.5: "volume/4050-{id}" vs
// "issue/4000-{id}"). It is a distinct constant, not a magic string, because
// normalizeVolumeID compares against it to reject an issue guid ("4000-...")
// sent where a volume id belongs, rather than silently mis-querying.
const resourceTypeVolume = "4050"

// ErrInvalidVolumeID means a value that was supposed to be a ComicVine
// volume id was neither a bare numeric id ("18257") nor a
// "<resource-type>-<numeric>" guid ("4050-18257"), or named a resource type
// other than a volume -- most commonly an issue guid ("4000-...") sent
// where a volume id belongs. It is a sentinel distinct from
// metadata.ErrNotFound: this is rejected before any request is sent, not
// after ComicVine answers.
var ErrInvalidVolumeID = errors.New("comicvine: invalid volume id")

var numericIDPattern = regexp.MustCompile(`^[0-9]+$`)

// normalizeVolumeID accepts either shape a ComicVine volume id can arrive
// in and returns both forms ComicVine's own endpoints need:
//
//   - guid: the full "4050-<num>" resource id GET /volume/{guid} requires
//     (ComicVine's own volume page URL, e.g.
//     https://comicvine.gamespot.com/batman/4050-18257/, and
//     pkg/metadata.Validate's comicVinePattern, already encode this
//     prefixed form as the crosswalk's canonical shape -- it is what a user
//     copies and what Comic.spec.sourceID is expected to hold).
//   - num: the bare numeric id ("18257") GET /issues/?filter=volume:{num}
//     requires -- ComicVine's filter syntax rejects the prefixed guid there
//     (confirmed against the ComicVine API forums: filtering issues by
//     volume uses the plain numeric id, never "4050-<num>").
//
// Both a bare numeric id and the full guid are accepted as input so a
// caller that already normalized (or one still passing the bare id some
// call sites used before this existed) both work; anything else --
// including a well-formed guid for a different resource type, most
// commonly an issue guid -- is rejected via ErrInvalidVolumeID rather than
// sent to ComicVine and left to fail there.
func normalizeVolumeID(id string) (guid string, num string, err error) {
	prefix, rest, hasPrefix := strings.Cut(id, "-")
	if !hasPrefix {
		if id == "" || !numericIDPattern.MatchString(id) {
			return "", "", fmt.Errorf("%w: %q", ErrInvalidVolumeID, id)
		}
		return resourceTypeVolume + "-" + id, id, nil
	}
	if prefix != resourceTypeVolume || rest == "" || !numericIDPattern.MatchString(rest) {
		return "", "", fmt.Errorf("%w: %q", ErrInvalidVolumeID, id)
	}
	return id, rest, nil
}

// userAgent is sent on every request: ComicVine is reported (community,
// unverified) to block the Go standard library's default
// "Go-http-client/..." User-Agent outright, so any non-empty custom value
// is sufficient -- this client does not need to imitate a browser.
const userAgent = "Clustarr/0.1 (+https://github.com/mediactl/clustarr)"

// ComicVine's envelope status codes. 1 and 100-105 are the table on
// ComicVine's own API documentation page
// (https://comicvine.gamespot.com/api/documentation, re-read for this
// change); 107 is not in that table but is the code ComicTagger's
// ComicVineTalker (vendored by Mylar3 as lib/comictaggerlib/
// comicvinetalker.py, ComicVineTalkerException.RateLimit = 107) waits on and
// retries as "rate limit exceeded".
const (
	statusOK               = 1
	statusInvalidAPIKey    = 100
	statusObjectNotFound   = 101
	statusURLFormatError   = 102
	statusJSONPCallback    = 103
	statusFilterError      = 104
	statusSubscriberOnly   = 105
	statusRateLimitReached = 107
)

// httpStatusSlowDown is the 420 ComicVine has been observed to answer an
// over-eager client with; ComicTagger's talker treats it exactly like 429
// (its TWITTER_TOO_MANY_REQUESTS).
const httpStatusSlowDown = 420

// continuingWindow is how recent a volume's latest issue must be for the
// volume to count as continuing: Mylar3's rule (mylar/helpers.py
// havetotals, verified via DeepWiki against mylar3/mylar3) -- a latest
// issue within 55 days of today is "Continuing", otherwise "Ended".
// ComicVine itself has no volume status field (its documented volume
// fields: aliases, count_of_issues, first_issue, last_issue, start_year,
// ... -- none is a status).
const continuingWindow = 55 * 24 * time.Hour

// Volume statuses, in the same lowercase vocabulary as
// metadata.SeriesStatus.
const (
	volumeStatusContinuing = "continuing"
	volumeStatusEnded      = "ended"
)

// Client is a metadata.ComicProvider backed by ComicVine.
type Client struct {
	http    *http.Client
	baseURL string
	apiKey  string
	limiter *rate.Limiter
}

// New builds a Client. httpClient may be nil, in which case
// http.DefaultClient is used. baseURL overrides the default ComicVine host
// -- tests pass an httptest.Server URL; production callers pass "".
func New(apiKey string, httpClient *http.Client, baseURL string, limiter *rate.Limiter) *Client {
	hc := httpClient
	if hc == nil {
		hc = http.DefaultClient
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{http: hc, baseURL: baseURL, apiKey: apiKey, limiter: limiter}
}

// Name identifies this provider for logging and metrics.
func (c *Client) Name() string { return "comicvine" }

// Capabilities describes what this provider supports.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{LookupBy: []string{metadata.KeyComicVine}, Search: true}
}

// searchVolumesResponse is ComicVine's status-enveloped search response.
type searchVolumesResponse struct {
	Results []struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		StartYear string `json:"start_year"`
		Image     struct {
			SuperURL string `json:"super_url"`
		} `json:"image"`
	} `json:"results"`
}

// SearchVolumes searches ComicVine's volume index by name.
func (c *Client) SearchVolumes(ctx context.Context, q string) ([]metadata.SearchHit, error) {
	ctx, span := tracing.Start(ctx, "metadata.comicvine.SearchVolumes")
	defer span.End()
	logger := logging.FromContext(ctx)

	var raw searchVolumesResponse
	query := url.Values{"resources": {"volume"}, "query": {q}, "field_list": {"id,name,start_year,image"}}
	if err := c.doGet(ctx, "/search/", query, &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "comicvine: search failed", "query", q, "error", err)
		return nil, err
	}

	hits := make([]metadata.SearchHit, 0, len(raw.Results))
	for _, r := range raw.Results {
		hit := metadata.SearchHit{
			IDs:    metadata.ExternalIDs{metadata.KeyComicVine: strconv.FormatInt(r.ID, 10)},
			Title:  r.Name,
			Poster: r.Image.SuperURL,
		}
		if year, err := strconv.Atoi(r.StartYear); err == nil {
			hit.Year = int32(year)
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

// volumeResponse is ComicVine's status-enveloped single-volume response
// (docs/research/metadata.md §2.5: every result is wrapped in a status
// envelope with error/status_code/results).
type volumeResponse struct {
	Results struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		StartYear string `json:"start_year"`
		Publisher struct {
			Name string `json:"name"`
		} `json:"publisher"`
		CountOfIssues int32  `json:"count_of_issues"`
		Description   string `json:"description"`
		LastIssue     *struct {
			ID int64 `json:"id"`
		} `json:"last_issue"`
	} `json:"results"`
}

// issueDatesResponse is ComicVine's single-issue response, narrowed by
// field_list to the two dates volumeStatus reads.
type issueDatesResponse struct {
	Results struct {
		CoverDate string `json:"cover_date"`
		StoreDate string `json:"store_date"`
	} `json:"results"`
}

// Volume fetches a single comic volume by its ComicVine id
// (ids[metadata.KeyComicVine], normalized by normalizeVolumeID -- see its
// doc comment for the accepted shapes and why. The GET /volume/{guid}
// endpoint always receives the full "4050-<num>" guid regardless of which
// form arrived, and the returned ComicVolume.IDs carries that same
// canonical guid back, so a caller that started from a bare numeric id
// still ends up with the canonical form once metadata has been fetched).
//
// Status is derived, because ComicVine has none: see volumeStatus.
func (c *Client) Volume(ctx context.Context, ids metadata.ExternalIDs) (*metadata.ComicVolume, error) {
	ctx, span := tracing.Start(ctx, "metadata.comicvine.Volume")
	defer span.End()
	logger := logging.FromContext(ctx)

	id, ok := ids[metadata.KeyComicVine]
	if !ok {
		err := fmt.Errorf("comicvine: Volume requires %q in ExternalIDs", metadata.KeyComicVine)
		tracing.RecordError(span, err)
		return nil, err
	}
	guid, _, err := normalizeVolumeID(id)
	if err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "comicvine: invalid volume id", "id", id, "error", err)
		return nil, err
	}

	var raw volumeResponse
	query := url.Values{"field_list": {"id,name,start_year,publisher,count_of_issues,description,site_detail_url,last_issue"}}
	if err := c.doGet(ctx, "/volume/"+guid, query, &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "comicvine: volume fetch failed", "id", guid, "error", err)
		return nil, err
	}

	v := &metadata.ComicVolume{
		IDs:         metadata.ExternalIDs{metadata.KeyComicVine: guid},
		Title:       raw.Results.Name,
		Description: raw.Results.Description,
		Publisher:   raw.Results.Publisher.Name,
		IssueCount:  raw.Results.CountOfIssues,
	}
	if year, err := strconv.Atoi(raw.Results.StartYear); err == nil {
		y := int32(year)
		v.StartYear = &y
	}
	if raw.Results.LastIssue != nil && raw.Results.LastIssue.ID > 0 {
		v.Status = c.volumeStatus(ctx, raw.Results.LastIssue.ID, time.Now())
	}

	logger.DebugContext(ctx, "comicvine: volume fetched", "id", guid, "title", v.Title, "status", v.Status)
	return v, nil
}

// volumeStatus derives a volume's status from its latest issue's date,
// Mylar3's way (see continuingWindow): fetch the volume's last_issue, take
// its store date (the on-sale date) or failing that its cover date, and
// call the volume continuing when that date is within continuingWindow of
// now -- or still in the future -- and ended otherwise.
//
// It costs one request, against ComicVine's separate "issue" resource
// budget. A failure there is logged and yields "" (unknown), not an error:
// the status is derived, it reaches no CRD field (ComicMetadata has none),
// and its only consumer is the gateway's refresh cadence, which treats ""
// as ongoing -- the conservative, shorter cadence. Failing the whole Volume
// over it would throw away the volume record the caller actually asked
// for.
func (c *Client) volumeStatus(ctx context.Context, lastIssueID int64, now time.Time) string {
	logger := logging.FromContext(ctx)

	var raw issueDatesResponse
	query := url.Values{"field_list": {"cover_date,store_date"}}
	if err := c.doGet(ctx, fmt.Sprintf("/issue/4000-%d/", lastIssueID), query, &raw); err != nil {
		logger.WarnContext(ctx, "comicvine: latest-issue fetch failed; volume status unknown", "issue_id", lastIssueID, "error", err)
		return ""
	}
	latest, err := time.Parse("2006-01-02", raw.Results.StoreDate)
	if err != nil {
		latest, err = time.Parse("2006-01-02", raw.Results.CoverDate)
	}
	if err != nil {
		return ""
	}
	if now.Sub(latest) < continuingWindow {
		return volumeStatusContinuing
	}
	return volumeStatusEnded
}

// issuesResponse is ComicVine's status-enveloped issue-list response.
type issuesResponse struct {
	Results []struct {
		ID          int64  `json:"id"`
		IssueNumber string `json:"issue_number"`
		Name        string `json:"name"`
		CoverDate   string `json:"cover_date"`
		StoreDate   string `json:"store_date"`
		Image       struct {
			SuperURL string `json:"super_url"`
		} `json:"image"`
	} `json:"results"`
}

// Issues fetches the issues belonging to volumeID. volumeID is normalized
// by normalizeVolumeID the same way Volume's id is -- accepting either the
// full "4050-<num>" guid or a bare numeric id -- but the GET
// /issues/?filter=volume:{num} request always uses the bare numeric form,
// since ComicVine's filter syntax rejects the prefixed guid there (unlike
// the /volume/{guid} path Volume calls). This is what makes Comic->Issue
// work when Comic.spec.sourceID (the canonical, prefixed form) is passed
// unchanged to both Volume and Issues: each endpoint gets the shape it
// actually needs regardless of which form arrived.
func (c *Client) Issues(ctx context.Context, volumeID string) ([]metadata.ComicIssue, error) {
	ctx, span := tracing.Start(ctx, "metadata.comicvine.Issues")
	defer span.End()
	logger := logging.FromContext(ctx)

	_, num, err := normalizeVolumeID(volumeID)
	if err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "comicvine: invalid volume id", "volume_id", volumeID, "error", err)
		return nil, err
	}

	var raw issuesResponse
	query := url.Values{"filter": {"volume:" + num}, "field_list": {"id,issue_number,name,cover_date,store_date,image"}}
	if err := c.doGet(ctx, "/issues/", query, &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "comicvine: issues fetch failed", "volume_id", num, "error", err)
		return nil, err
	}

	issues := make([]metadata.ComicIssue, 0, len(raw.Results))
	for _, r := range raw.Results {
		issue := metadata.ComicIssue{
			IDs:    metadata.ExternalIDs{metadata.KeyComicVine: strconv.FormatInt(r.ID, 10)},
			Number: r.IssueNumber,
			Title:  r.Name,
		}
		if t, err := time.Parse("2006-01-02", r.CoverDate); err == nil {
			issue.CoverDate = &t
		}
		if t, err := time.Parse("2006-01-02", r.StoreDate); err == nil {
			issue.StoreDate = &t
		}
		if r.Image.SuperURL != "" {
			issue.Image = &metadata.Image{Type: metadata.ImageTypeThumb, URL: r.Image.SuperURL}
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

var _ metadata.ComicProvider = (*Client)(nil)

// envelope is the status wrapper around every ComicVine response.
type envelope struct {
	Error      string `json:"error"`
	StatusCode int    `json:"status_code"`
}

// doGet issues a GET request for resource (a path relative to c.baseURL)
// with query plus the api_key and format parameters, waiting on the rate
// limiter and setting a non-default User-Agent (ComicVine is reported to
// block Go's default one). The body is read through metadata.ReadBody's cap
// whatever the HTTP status, because ComicVine explains a failure in the
// envelope: an HTTP 401 carries status_code 100, and a 200 can carry any
// non-1 code. See mapStatusCode.
func (c *Client) doGet(ctx context.Context, resource string, query url.Values, out any) error {
	if err := c.limiter.Wait(ctx); err != nil {
		return err
	}
	q := url.Values{}
	for k, v := range query {
		q[k] = v
	}
	q.Set("api_key", c.apiKey)
	q.Set("format", "json")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+resource+"?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("comicvine: build request for %s: %w", resource, err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		// *url.Error's message is the whole request URL, api_key included.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("comicvine: %s: %w", resource, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := metadata.ReadBody(resp.Body, metadata.MaxResponseBytes)
	if err != nil {
		return fmt.Errorf("comicvine: %s: %w", resource, err)
	}
	var env envelope
	envErr := json.Unmarshal(body, &env)

	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == httpStatusSlowDown:
		return &metadata.RateLimitedError{Provider: "comicvine", RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	case resp.StatusCode == http.StatusOK:
		if envErr != nil {
			return fmt.Errorf("comicvine: %s: %w: %w", resource, metadata.ErrDecode, envErr)
		}
		if env.StatusCode == 0 {
			return fmt.Errorf("comicvine: %s: %w: no status_code envelope", resource, metadata.ErrDecode)
		}
		if env.StatusCode != statusOK {
			return mapStatusCode(resource, env)
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("comicvine: %s: %w: %w", resource, metadata.ErrDecode, err)
		}
		return nil
	case envErr == nil && env.StatusCode != 0 && env.StatusCode != statusOK:
		return mapStatusCode(resource, env)
	case resp.StatusCode == http.StatusNotFound:
		return metadata.ErrNotFound
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("comicvine: %s: HTTP %d: %w", resource, resp.StatusCode, metadata.ErrAuth)
	default:
		return fmt.Errorf("comicvine: unexpected status %d for %s", resp.StatusCode, resource)
	}
}

// mapStatusCode maps a non-1 envelope status_code onto metadata's
// sentinels: 100 (invalid API key) and 105 (subscriber-only content) are
// ErrAuth -- both are "these credentials may not have this"; 101 (object
// not found) is ErrNotFound; 107 (rate limit exceeded) is ErrRateLimited.
// 102-104 (malformed URL, JSONP callback, bad filter) are this client's own
// request bugs and stay plain errors carrying ComicVine's message, as does
// any code outside the table. A 200 whose envelope says 104 used to decode
// as a valid, empty result list.
func mapStatusCode(resource string, env envelope) error {
	switch env.StatusCode {
	case statusInvalidAPIKey, statusSubscriberOnly:
		return fmt.Errorf("comicvine: %s: status_code %d %q: %w", resource, env.StatusCode, env.Error, metadata.ErrAuth)
	case statusObjectNotFound:
		return fmt.Errorf("comicvine: %s: status_code %d %q: %w", resource, env.StatusCode, env.Error, metadata.ErrNotFound)
	case statusRateLimitReached:
		return &metadata.RateLimitedError{Provider: "comicvine"}
	case statusURLFormatError, statusJSONPCallback, statusFilterError:
		return fmt.Errorf("comicvine: %s: request rejected, status_code %d %q", resource, env.StatusCode, env.Error)
	default:
		return fmt.Errorf("comicvine: %s: status_code %d %q", resource, env.StatusCode, env.Error)
	}
}

// parseRetryAfter parses a Retry-After header value (seconds) into a
// duration, defaulting to zero when absent or malformed.
func parseRetryAfter(v string) time.Duration {
	seconds, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
