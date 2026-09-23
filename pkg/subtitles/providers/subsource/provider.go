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

// Package subsource implements subtitles.Provider against SubSource
// (api.subsource.net), the JSON-API provider research note §4.2 lists as
// viable for movies and TV.
//
// It is a port of Bazarr's provider,
// custom_libs/subliminal_patch/providers/subsource.py, with its language
// converter (converters/subsource.py), from morpheus65535/bazarr at
// ec41fe82c03ccd556d168666595bd0be1202f77b: the title-id lookup
// (movies/search, by IMDb id and then by text, matched on title and year),
// the one-language subtitle listing, the HI and forced heuristics, the
// season/episode filter over release names, and the status mapping. Field
// names are the ones that code reads. The API key travels in the X-API-Key
// header, which the API itself names as an alternative to the api_key
// query parameter Bazarr uses (verified live on 2026-09-23: a keyless
// request gets 401 "Please provide an API key in the X-API-Key header or
// api_key query parameter"), so it never appears in a URL or an error.
package subsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/internal/subarchive"
)

const (
	// DefaultEndpoint is SubSource's API base (Bazarr's _server_url()).
	DefaultEndpoint = "https://api.subsource.net/api/v1"

	// titlesTTL is how long a resolved title id is reused: Bazarr caches
	// search_titles for TITLES_EXPIRATION_TIME, six hours.
	titlesTTL = 6 * time.Hour
	// listLimit is the listing size Bazarr asks for.
	listLimit = 100

	maxJSONBytes      = 4 << 20  // a title search or a 100-item listing
	maxDownloadBytes  = 32 << 20 // an archive, possibly a season pack
	maxSubtitleBytes  = 8 << 20  // one subtitle file inside it
	maxErrorBodyBytes = 4 << 10
)

var (
	// ErrResponseTooLarge is returned when a response body exceeds its cap.
	ErrResponseTooLarge = errors.New("subtitles: subsource: response body exceeds size limit")
	// ErrRejected is a response about one request only -- a subtitle that
	// no longer exists. It is deliberately not a subtitles.ProviderError,
	// so the caller does not throttle the provider over it.
	ErrRejected = errors.New("subtitles: subsource: request rejected")
	// ErrNoSubtitle is returned by Download when the archive holds no
	// subtitle for the candidate -- also about the one candidate only.
	ErrNoSubtitle = errors.New("subtitles: subsource: no subtitle in download")
)

// Config configures a Provider.
type Config struct {
	// APIKey is the SubSource API key. Required.
	APIKey string
	// UserAgent is sent on every request. Empty gets "clustarr".
	UserAgent string
	// Endpoint overrides DefaultEndpoint, for a mirror or a test server.
	Endpoint string
	// HTTPClient defaults to http.DefaultClient.
	HTTPClient *http.Client
	// Limiter, if set, paces every outbound request on the caller's budget.
	// It defaults to nil -- no client-side pacing -- because the caller
	// owns rate limiting (CLAUDE.md, ruling R3).
	Limiter *rate.Limiter
}

// Provider implements subtitles.Provider against SubSource. It is safe for
// concurrent use.
type Provider struct {
	cfg Config
	now func() time.Time

	mu     sync.Mutex
	titles map[titleKey]titleEntry
}

type titleKey struct {
	imdb, title string
	season      int
	year        int
}

type titleEntry struct {
	id      string
	idMatch bool
	at      time.Time
}

// New builds a Provider from cfg, applying defaults for any zero field.
func New(cfg Config) *Provider {
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &Provider{cfg: cfg, now: time.Now, titles: map[titleKey]titleEntry{}}
}

// Name is the provider type, which subtitles.ThrottleFor keys its
// subsource row on.
func (p *Provider) Name() string { return "subsource" }

// HIVerifiable is SubsourceSubtitle.hearing_impaired_verifiable.
func (p *Provider) HIVerifiable() bool { return true }

// Capabilities: movies and episodes, forced and HI variants; no hash
// search (hash_verifiable = False).
func (p *Provider) Capabilities() subtitles.Capabilities {
	return subtitles.Capabilities{
		Movies: true, Episodes: true, ForcedSearch: true,
		Languages:    func(tag string) bool { _, ok := toSubSource(tag); return ok },
		NeedsSecrets: []string{"apiKey"},
	}
}

func (p *Provider) wait(ctx context.Context) error {
	if p.cfg.Limiter == nil {
		return nil
	}
	return p.cfg.Limiter.Wait(ctx)
}

// titleResult is one movies/search result. Each season of a show is its
// own entry.
type titleResult struct {
	MovieID        flexID  `json:"movieId"`
	Title          *string `json:"title"`
	AlternateTitle string  `json:"alternateTitle"`
	ReleaseYear    flexID  `json:"releaseYear"`
}

// listItem is one subtitle in a /subtitles listing.
type listItem struct {
	SubtitleID      flexID   `json:"subtitleId"`
	Link            string   `json:"link"`
	Language        string   `json:"language"`
	ReleaseInfo     []string `json:"releaseInfo"`
	Commentary      *string  `json:"commentary"`
	HearingImpaired bool     `json:"hearingImpaired"`
	ForeignParts    bool     `json:"foreignParts"`
	UploaderID      flexID   `json:"uploaderId"`
	Contributors    []struct {
		ID          flexID `json:"id"`
		DisplayName string `json:"displayname"`
	} `json:"contributors"`
}

// flexID decodes an id or year sent as a number or a string, keeping its
// text: a listing is not worth failing over the type of one field.
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		*f = flexID(n.String())
		return nil
	}
	*f = ""
	return nil
}

// Search implements subtitles.Provider.Search (Bazarr's query()).
func (p *Provider) Search(ctx context.Context, q subtitles.Query) ([]subtitles.Candidate, error) {
	ctx, span := tracing.Start(ctx, "subtitles.subsource.search")
	defer span.End()

	if p.cfg.APIKey == "" {
		return nil, &subtitles.ProviderError{Provider: p.Name(), Kind: subtitles.KindConfig, Err: errors.New("an API key must be specified")}
	}

	// Bazarr searches one language: the first asked for (here, the first
	// SubSource has a name for).
	var name string
	var forced, hi bool
	for _, k := range q.Languages {
		tag, f, h, err := subtitles.ParseLangKey(k)
		if err != nil {
			return nil, err
		}
		if n, ok := toSubSource(tag); ok {
			name, forced, hi = n, f, h
			break
		}
	}
	if name == "" {
		return nil, nil
	}

	episode := q.Kind == common.MediaKindEpisode
	imdb := q.IDs["imdb"]
	season := 0
	if episode {
		imdb, season = q.IDs["parent_imdb"], q.Season
	}
	imdb = imdbTT(imdb)
	// Bazarr looks a title up only when it has the IMDb id; the text search
	// is that lookup's fallback, not a search of its own.
	if imdb == "" {
		return nil, nil
	}
	titleID, idMatch, err := p.titleID(ctx, q.Title, imdb, season, q.Year)
	if err != nil {
		return nil, err
	}
	if titleID == "" {
		return nil, nil
	}

	params := url.Values{
		"language": {name}, "limit": {strconv.Itoa(listLimit)}, "movieId": {titleID},
	}
	if episode {
		params.Set("seasonNumber", strconv.Itoa(q.Season))
		params.Set("episodeNumber", strconv.Itoa(q.Episode))
	}
	var listing struct {
		Success json.RawMessage `json:"success"`
		Data    []listItem      `json:"data"`
	}
	if err := p.getJSON(ctx, "/subtitles?"+params.Encode(), &listing); err != nil {
		if errors.Is(err, ErrRejected) {
			return nil, nil
		}
		return nil, err
	}
	if s := strings.TrimSpace(string(listing.Success)); s == "false" {
		return nil, nil
	}

	out := make([]subtitles.Candidate, 0, len(listing.Data))
	for _, it := range listing.Data {
		isForced, isHI := it.forced(), it.hearingImpaired()
		if isForced != forced || (hi && !isHI) {
			continue // the same variant rules opensubtitlescom and subdl apply
		}
		tag, ok := tagOf(it.Language)
		if !ok {
			continue
		}
		if it.SubtitleID == "" {
			continue
		}
		h := handle{ID: string(it.SubtitleID)}
		if episode {
			// Bazarr keeps a result whose release names spell the season
			// and either this episode or none -- a season pack.
			s, e := seasonEpisode(it.ReleaseInfo)
			if s != q.Season || (e != 0 && e != q.Episode) {
				continue
			}
			h.Season, h.Episode, h.Pack = q.Season, q.Episode, e == 0
		}
		out = append(out, subtitles.Candidate{
			Provider: p.Name(), ID: h.ID, FetchID: h.encode(), Language: tag,
			HI: isHI, Forced: isForced, ReleaseInfo: strings.Join(it.ReleaseInfo, "\n"),
			Matches:  matches(q, it.ReleaseInfo, idMatch),
			Uploader: it.uploader(),
		})
	}
	return out, nil
}

// titleID is Bazarr's search_titles: the SubSource movieId for the item,
// found by IMDb id and, when that finds nothing, by text; a result counts
// when one of its titles contains the item's title and its year matches the
// item's (when the item has one; titleYearMatches). idMatch reports that the IMDb search
// found it. Results are cached for titlesTTL, as Bazarr caches them.
func (p *Provider) titleID(ctx context.Context, title, imdb string, season, year int) (id string, idMatch bool, err error) {
	key := titleKey{imdb: imdb, title: strings.ToLower(title), season: season, year: year}
	p.mu.Lock()
	if e, ok := p.titles[key]; ok && p.now().Sub(e.at) < titlesTTL {
		p.mu.Unlock()
		return e.id, e.idMatch, nil
	}
	p.mu.Unlock()

	search := func(params url.Values) ([]titleResult, error) {
		if season > 0 {
			params.Set("season", strconv.Itoa(season))
		}
		var res struct {
			Data []titleResult `json:"data"`
		}
		if err := p.getJSON(ctx, "/movies/search?"+params.Encode(), &res); err != nil {
			if errors.Is(err, ErrRejected) {
				return nil, nil
			}
			return nil, err
		}
		return res.Data, nil
	}

	results, err := search(url.Values{"searchType": {"imdb"}, "imdb": {imdb}})
	if err != nil {
		return "", false, err
	}
	idMatch = len(results) > 0
	if !idMatch && title != "" {
		logging.FromContext(ctx).Debug("subsource: no title for the IMDb id; searching by text", "imdb", imdb)
		if results, err = search(url.Values{"searchType": {"text"}, "q": {strings.ToLower(title)}}); err != nil {
			return "", false, err
		}
	}
	want := strings.ToLower(title)
	for _, r := range results {
		if r.Title == nil || r.ReleaseYear == "" || r.MovieID == "" {
			continue
		}
		names := []string{strings.ToLower(*r.Title)}
		if r.AlternateTitle != "" {
			names = append(names, strings.ToLower(r.AlternateTitle))
		}
		matched := false
		for _, n := range names {
			matched = matched || (want != "" && strings.Contains(n, want))
		}
		if !matched {
			continue
		}
		if y, err := strconv.Atoi(string(r.ReleaseYear)); year == 0 || (err == nil && titleYearMatches(y, year, season, idMatch)) {
			id = string(r.MovieID)
			break
		}
	}
	idMatch = idMatch && id != ""

	p.mu.Lock()
	p.titles[key] = titleEntry{id: id, idMatch: idMatch, at: p.now()}
	p.mu.Unlock()
	return id, idMatch, nil
}

// titleYearMatches is the year half of titleID's match. Bazarr compares the
// result's releaseYear with the item's year exactly (search_titles, `not
// self.video.year or self.video.year == int(result['releaseYear'])`). For a
// show that finds nothing past its first season: each season is its own
// movies/search entry carrying that SEASON's year, while the item's year is
// the series' premiere, so season 5 of a 2008 show is a 2012 entry. When the
// IMDb search found the entries the id already pins the show, and a season
// can only air in its series' premiere year or later, so any year from the
// premiere on is that show's season. A text-search result keeps Bazarr's
// exact rule: there the year is all that tells a show from a same-titled
// remake, whose season N can air long after the original premiered.
func titleYearMatches(resultYear, itemYear, season int, idMatch bool) bool {
	if resultYear == itemYear {
		return true
	}
	return season > 0 && idMatch && resultYear > itemYear
}

// matches is SubsourceSubtitle.get_matches in this package's Match keys.
// Bazarr claims the IMDb id for every result (series_imdb_id or imdb_id,
// which its score.py expands to series or title plus year). Here the id is
// claimed -- series or title, and year -- only when the IMDb search found
// the title; a title found by text search is the series or title alone,
// plus the year when the lookup checked one. An episode result was kept
// only for this season and for this episode or a season pack (see Search),
// which is what guessit's season and episode matches, and Bazarr's pack
// rule, give it. The release names add what each corroborates about the
// target release (utils.update_matches).
func matches(q subtitles.Query, releases []string, idMatch bool) map[string]bool {
	m := map[string]bool{}
	if q.Kind == common.MediaKindEpisode {
		m[subtitles.MatchSeries] = true
		m[subtitles.MatchSeason] = true
		m[subtitles.MatchEpisode] = true
	} else {
		m[subtitles.MatchTitle] = true
	}
	if idMatch || q.Year > 0 {
		m[subtitles.MatchYear] = true
	}
	for _, r := range releases {
		for k, v := range subtitles.GuessMatches(q.Kind, q.Release, r) {
			m[k] = m[k] || v
		}
	}
	return m
}

// seasonEpisode is Bazarr's _get_season_episode_from_release_info: the
// first season and the first episode any release name spells.
func seasonEpisode(releases []string) (season, episode int) {
	for _, r := range releases {
		if season != 0 && episode != 0 {
			break
		}
		p, err := release.Parse(r, release.Options{Kind: common.MediaKindEpisode})
		if err != nil {
			continue
		}
		if season == 0 && len(p.Seasons) > 0 {
			season = p.Seasons[0]
		}
		if episode == 0 && len(p.Episodes) > 0 {
			episode = p.Episodes[0]
		}
	}
	return season, episode
}

func (it listItem) commentary() string {
	if it.Commentary == nil {
		return ""
	}
	return strings.ToLower(*it.Commentary)
}

// Bazarr's _is_hi tag lists, verbatim.
var (
	nonHITags = []string{"hi remove", "non hi", "nonhi", "non-hi", "non-sdh", "non sdh", "nonsdh", "sdh remove"}
	hiTags    = []string{"_hi_", " hi ", ".hi.", "hi ", " hi", "sdh", "𝓢𝓓𝓗", "_cc_", " cc ", ".cc.", "closed caption"}
)

// hearingImpaired is Bazarr's _is_hi: the API's flag, else the commentary
// -- a non-HI marker first, then an HI one.
func (it listItem) hearingImpaired() bool {
	if it.HearingImpaired {
		return true
	}
	c := it.commentary()
	for _, t := range nonHITags {
		if strings.Contains(c, t) {
			return false
		}
	}
	for _, t := range hiTags {
		if strings.Contains(c, t) {
			return true
		}
	}
	return false
}

// forced is Bazarr's _is_forced: the API's foreignParts flag, else
// "forced" or "foreign" in the commentary.
func (it listItem) forced() bool {
	c := it.commentary()
	return it.ForeignParts || strings.Contains(c, "forced") || strings.Contains(c, "foreign")
}

// uploader is Bazarr's _get_uploader_name: the contributor whose id is the
// uploader's.
func (it listItem) uploader() string {
	for _, c := range it.Contributors {
		if c.ID == it.UploaderID {
			return c.DisplayName
		}
	}
	return ""
}

// handle is what Download needs, carried opaquely in Candidate.FetchID.
type handle struct {
	ID      string `json:"id"`
	Pack    bool   `json:"p,omitempty"`
	Season  int    `json:"s,omitempty"`
	Episode int    `json:"e,omitempty"`
}

func (h handle) encode() string {
	b, _ := json.Marshal(h)
	return string(b)
}

// Download implements subtitles.Provider.Download (Bazarr's
// download_subtitle): GET /subtitles/{id}/download is an archive, from
// which the subtitle for the candidate is taken.
//
// Bazarr's archive selection (ProviderSubtitleArchiveMixin) ends by falling
// back to any subtitle in the archive when none names the episode. That is
// a guess -- the wrong episode, served as this one -- so here, as in the
// SubDL client, an archive with several subtitles and none for the episode
// is ErrNoSubtitle.
func (p *Provider) Download(ctx context.Context, c subtitles.Candidate) ([]byte, string, error) {
	ctx, span := tracing.Start(ctx, "subtitles.subsource.download")
	defer span.End()

	var h handle
	if err := json.Unmarshal([]byte(c.FetchID), &h); err != nil || h.ID == "" {
		return nil, "", fmt.Errorf("subtitles: subsource: invalid FetchID %q", c.FetchID)
	}
	resp, err := p.get(ctx, "/subtitles/"+url.PathEscape(h.ID)+"/download")
	if err != nil {
		tracing.RecordError(span, err)
		return nil, "", err
	}
	raw, err := readCapped(resp.Body, maxDownloadBytes)
	_ = resp.Body.Close()
	if err != nil {
		tracing.RecordError(span, err)
		return nil, "", fmt.Errorf("subtitles: subsource download: %w", err)
	}

	a, err := subarchive.Open(raw, maxSubtitleBytes)
	if err != nil {
		// Bazarr: "Could not unzip subtitle" -- this candidate's defect.
		return nil, "", fmt.Errorf("%w: %w", ErrNoSubtitle, err)
	}
	name, err := subarchive.Pick(a.Names(), h.Season, h.Episode, c.Forced)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrNoSubtitle, err)
	}
	b, err := a.Read(name)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrNoSubtitle, err)
	}
	return b, path.Base(name), nil
}

// getJSON GETs pathAndQuery and decodes the capped body into v.
func (p *Provider) getJSON(ctx context.Context, pathAndQuery string, v any) error {
	resp, err := p.get(ctx, pathAndQuery)
	if err != nil {
		return err
	}
	raw, err := readCapped(resp.Body, maxJSONBytes)
	_ = resp.Body.Close()
	if err != nil {
		return fmt.Errorf("subtitles: subsource: %w", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return &subtitles.ProviderError{Provider: p.Name(), Kind: subtitles.KindParse, Err: err}
	}
	return nil
}

// get performs one request with the key in the X-API-Key header and maps a
// non-200 status (Bazarr's checked and _status_raiser).
func (p *Provider) get(ctx context.Context, pathAndQuery string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.Endpoint+pathAndQuery, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", p.cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	ua := p.cfg.UserAgent
	if ua == "" {
		ua = "clustarr"
	}
	req.Header.Set("User-Agent", ua)
	if err := p.wait(ctx); err != nil {
		return nil, err
	}
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("subtitles: subsource: %w", err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	return nil, p.statusError(resp)
}

// statusError is Bazarr's _status_raiser and checked(), less the inline
// sleep:
//
//	400 -> APIThrottled ("Invalid request parameters")
//	401 -> Auth (AuthenticationError; the subsource row holds it 1 h)
//	403 -> Auth, for 15 min (ForbiddenError, which has no Kind of its own)
//	429 -> TooManyRequests, for exactly as long as the server says: its
//	       X-RateLimit-Reset timestamp, else the body's retryAfter (at least
//	       a second). Bazarr waits a reset under a minute out inline and
//	       retries once; benching the provider for the same delay is that
//	       without blocking the caller. With neither, the table's hour.
//	404 -> ErrRejected, about this request only
//	other -> ServiceUnavailable
func (p *Provider) statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	var payload struct {
		Error      string          `json:"error"`
		Message    string          `json:"message"`
		RetryAfter json.RawMessage `json:"retryAfter"`
	}
	_ = json.Unmarshal(body, &payload)
	msg := payload.Message
	if msg == "" {
		msg = payload.Error
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	pe := &subtitles.ProviderError{Provider: p.Name(), Err: fmt.Errorf("http %d: %s", resp.StatusCode, msg)}
	switch resp.StatusCode {
	case http.StatusBadRequest:
		pe.Kind = subtitles.KindAPIThrottled
	case http.StatusUnauthorized:
		pe.Kind = subtitles.KindAuth
	case http.StatusForbidden:
		pe.Kind, pe.RetryAfter = subtitles.KindAuth, 15*time.Minute
	case http.StatusTooManyRequests:
		pe.Kind = subtitles.KindTooManyRequests
		pe.RetryAfter = p.retryAfter(resp, payload.RetryAfter)
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrRejected, msg)
	default:
		pe.Kind = subtitles.KindServiceUnavailable
	}
	return pe
}

// retryAfter is Bazarr's _retry_after: the X-RateLimit-Reset timestamp,
// which stays right however long the response waited, else the body's
// numeric retryAfter; never less than a second once the server has said
// anything, since a reset already past still means it is refusing. Zero
// when it said nothing.
func (p *Provider) retryAfter(resp *http.Response, bodyRetry json.RawMessage) time.Duration {
	var d time.Duration
	known := false
	if s := resp.Header.Get("X-RateLimit-Reset"); s != "" {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			d, known = t.Sub(p.now()), true
		}
	}
	if !known {
		var secs float64
		if json.Unmarshal(bodyRetry, &secs) == nil {
			d, known = time.Duration(math.Ceil(secs))*time.Second, true
		}
	}
	if !known {
		return 0
	}
	return max(d.Round(time.Second), time.Second)
}

func readCapped(body io.Reader, max int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > max {
		return nil, fmt.Errorf("%w: at least %d bytes", ErrResponseTooLarge, len(raw))
	}
	return raw, nil
}

// imdbTT formats an IMDb id as "tt" and at least seven digits from any of
// "tt0903747", "0903747" or "903747"; "" for anything else.
func imdbTT(id string) string {
	s := strings.TrimLeft(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(id)), "tt"), "0")
	if s == "" {
		return ""
	}
	if _, err := strconv.ParseUint(s, 10, 64); err != nil {
		return ""
	}
	if len(s) < 7 {
		s = strings.Repeat("0", 7-len(s)) + s
	}
	return "tt" + s
}
