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

// Package subdl implements subtitles.Provider against SubDL
// (api.subdl.com), the Subscene successor research note §4.2 lists as a
// viable JSON-API provider for movies and TV.
//
// It is a port of Bazarr's provider,
// custom_libs/subliminal_patch/providers/subdl.py, with its language
// converter (converters/subdl.py), from morpheus65535/bazarr at
// ec41fe82c03ccd556d168666595bd0be1202f77b. Field names and parameters are
// the ones that code sends and reads, and the payloads in its tests
// (tests/subliminal_patch/test_subdl.py) are what this package's fixtures
// under test/data/subtitles/subdl reproduce. The error body was verified
// live against api.subdl.com on 2026-09-23 (see errorPayload).
//
// What is ported: the search with its fallbacks (episode, then season-only,
// then title-only for TV; IMDb or film name, then TMDB, for movies),
// two-page pagination, per-episode pack handling through the server's
// unpacked files, Bazarr's HI and forced heuristics, identity matches, and
// the status mapping (see statusError). What is not: SubDL Pro's on-demand
// AI translation (a paid, job-polling flow), searches by absolute episode
// number, which subtitles.Query does not carry, and Bazarr's bazarr=1
// integration flag (see Search) -- so the server's bazarr_policy block,
// which answers that flag, is honoured only if the server sends one
// unasked; otherwise defaultPolicy applies. AI-translated results are
// returned flagged Candidate.AITranslated, for the caller's own filter to
// decide on.
package subdl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/internal/subarchive"
)

const (
	// DefaultEndpoint is SubDL's API base (Bazarr's server_url()).
	DefaultEndpoint = "https://api.subdl.com/api/v1"
	// DefaultDownloadEndpoint is the host download links are relative to
	// (Bazarr: urljoin("https://dl.subdl.com", subtitle.download_link)).
	DefaultDownloadEndpoint = "https://dl.subdl.com"

	// subsPerPage is the API's own cap on subs_per_page.
	subsPerPage = 30
	// maxPages is how deep the primary search pages: every page is a
	// request against the account's daily API quota, so Bazarr fetches a
	// second page only when the first came back full.
	maxPages = 2
)

// Config configures a Provider.
type Config struct {
	// APIKey is the SubDL API key. Required; it is sent as the api_key
	// query parameter, the only way SubDL accepts it.
	APIKey string
	// UserAgent is sent on every request. Empty gets "clustarr".
	UserAgent string
	// Endpoint overrides DefaultEndpoint, for a mirror or a test server.
	Endpoint string
	// DownloadEndpoint overrides the base download links are resolved
	// against. Empty means DefaultDownloadEndpoint when Endpoint is also
	// the default, and otherwise Endpoint's own scheme and host -- so a
	// single SubtitleProvider.spec.endpoint pointing at a mirror or an
	// in-cluster fixture serves both halves.
	DownloadEndpoint string
	// HTTPClient defaults to http.DefaultClient.
	HTTPClient *http.Client
	// Limiter, if set, paces every outbound request on the caller's budget.
	// It defaults to nil -- no client-side pacing -- because the caller
	// owns rate limiting (CLAUDE.md, ruling R3): captionarr paces through
	// the shared clustarr-provider-throttle token bucket.
	Limiter *rate.Limiter
}

// Provider implements subtitles.Provider against SubDL. It is safe for
// concurrent use.
type Provider struct {
	cfg Config
	now func() time.Time

	mu     sync.Mutex
	policy policy
}

// policy is the server-steered search behaviour of Bazarr's
// DEFAULT_BAZARR_POLICY, updated from a search's first page if that page
// carries a bazarr_policy block. Clustarr does not send the bazarr=1 flag
// the block answers (see Search), so in practice defaultPolicy holds.
type policy struct {
	enabled, seasonFallback, titleFallback, unpack bool
	maxPages                                       int
}

var defaultPolicy = policy{enabled: true, seasonFallback: true, titleFallback: true, unpack: true, maxPages: maxPages}

// New builds a Provider from cfg, applying defaults for any zero field.
func New(cfg Config) *Provider {
	if cfg.Endpoint == "" {
		cfg.Endpoint = DefaultEndpoint
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	if cfg.DownloadEndpoint == "" {
		cfg.DownloadEndpoint = DefaultDownloadEndpoint
		if cfg.Endpoint != DefaultEndpoint {
			if u, err := url.Parse(cfg.Endpoint); err == nil && u.Host != "" {
				cfg.DownloadEndpoint = u.Scheme + "://" + u.Host
			}
		}
	}
	cfg.DownloadEndpoint = strings.TrimRight(cfg.DownloadEndpoint, "/")
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &Provider{cfg: cfg, now: time.Now, policy: defaultPolicy}
}

func (p *Provider) wait(ctx context.Context) error {
	if p.cfg.Limiter == nil {
		return nil
	}
	return p.cfg.Limiter.Wait(ctx)
}

func (p *Provider) userAgent() string {
	if p.cfg.UserAgent != "" {
		return p.cfg.UserAgent
	}
	return "clustarr"
}

// Name is the provider type, which subtitles.ThrottleFor keys its subdl
// row on.
func (p *Provider) Name() string { return "subdl" }

// HIVerifiable is SubdlSubtitle.hearing_impaired_verifiable (research note
// §4.1 lists subdl among the providers whose HI flag is trustworthy).
func (p *Provider) HIVerifiable() bool { return true }

// Capabilities: movies and episodes, forced and HI variants of every
// language SubDL has a code for; no hash search (hash_verifiable = False).
func (p *Provider) Capabilities() subtitles.Capabilities {
	return subtitles.Capabilities{
		Movies: true, Episodes: true, ForcedSearch: true,
		Languages:    func(tag string) bool { _, ok := toSubDL(tag); return ok },
		NeedsSecrets: []string{"apiKey"},
	}
}

// wanted is one requested language: its SubDL code and variant.
type wanted struct {
	code       string
	forced, hi bool
}

// accepts mirrors how opensubtitlescom maps a LangKey: a forced key takes
// only forced subtitles, a plain or HI key takes none; an HI key takes only
// HI ones and a plain key either (the caller's HI policy decides).
func (w wanted) accepts(code string, forced, hi bool) bool {
	return w.code == code && w.forced == forced && (!w.hi || hi)
}

// Search implements subtitles.Provider.Search (Bazarr's query()).
func (p *Provider) Search(ctx context.Context, q subtitles.Query) ([]subtitles.Candidate, error) {
	ctx, span := tracing.Start(ctx, "subtitles.subdl.search")
	defer span.End()

	if p.cfg.APIKey == "" {
		return nil, &subtitles.ProviderError{Provider: p.Name(), Kind: subtitles.KindConfig, Err: errors.New("an API key must be specified")}
	}

	var want []wanted
	codes := map[string]bool{}
	for _, k := range q.Languages {
		tag, forced, hi, err := subtitles.ParseLangKey(k)
		if err != nil {
			return nil, err
		}
		if code, ok := toSubDL(tag); ok {
			want = append(want, wanted{code, forced, hi})
			codes[code] = true
		}
	}
	if len(want) == 0 {
		return nil, nil // no language SubDL has a code for
	}
	langs := make([]string, 0, len(codes))
	for c := range codes {
		langs = append(langs, c)
	}
	sort.Strings(langs)

	episode := q.Kind == common.MediaKindEpisode
	var imdb, tmdb string
	if episode {
		imdb = imdbTT(q.IDs["parent_imdb"])
	} else {
		imdb, tmdb = imdbTT(q.IDs["imdb"]), numeric(q.IDs["tmdb"])
	}
	title := sanitizeTitle(q.Title)

	// Only parameters SubDL's own API documentation describes
	// (https://subdl.com/api-doc, "Request Parameters", read 2026-09-23):
	// comment, releases, hi and unpack each ask for one more field per
	// result. Bazarr also sends bazarr=1, which that page does not document
	// at all -- it lists a separate `client` parameter for naming an
	// integration, with "bazarr" as one of its values -- so it is an
	// integration flag, not a general filter, and Clustarr does not send
	// it: this client must not present itself as Bazarr. What it bought
	// Bazarr is covered here instead: image-based and .txt subtitles are
	// dropped when an archive is opened (subarchive keeps only .srt, .sub,
	// .ssa and .ass members), and without a bazarr_policy block the search
	// follows defaultPolicy -- Bazarr's own DEFAULT_BAZARR_POLICY. hi=1 is
	// sent because the documentation names it as what includes each
	// result's hearing-impaired flag, which HIVerifiable promises.
	base := url.Values{
		"comment": {"1"}, "releases": {"1"}, "hi": {"1"}, "unpack": {"1"},
		"languages": {strings.Join(langs, ",")},
	}
	switch {
	case imdb != "":
		base.Set("imdb_id", imdb)
	case title != "":
		base.Set("film_name", title)
	case tmdb == "":
		// Nothing identifies the item: a search on languages alone would
		// return other titles' subtitles.
		return nil, nil
	}
	// Only an id-backed result may claim the id's matches, which are worth
	// a large score; a film_name search is fuzzy server-side.
	matchedID, matchedTitle := imdb != "", imdb == "" && title != ""

	s := &searchRun{p: p}
	with := func(kv ...string) url.Values {
		v := url.Values{}
		for k, vs := range base {
			v[k] = vs
		}
		for i := 0; i+1 < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		return v
	}

	if episode {
		if err := s.search(ctx, with("type", "tv", "season_number", strconv.Itoa(q.Season), "episode_number", strconv.Itoa(q.Episode)), true, true); err != nil {
			return nil, err
		}
		pol := p.currentPolicy()
		if !pol.enabled {
			return nil, nil
		}
		// Split-season and cour-numbered uploads store an episode under
		// another number; the release names identify it.
		if pol.seasonFallback {
			if err := s.search(ctx, with("type", "tv", "season_number", strconv.Itoa(q.Season)), false, false); err != nil {
				return nil, err
			}
		}
		// Anime stored as season 0 is excluded by any season filter.
		if len(s.items) == 0 && pol.titleFallback {
			if err := s.search(ctx, with("type", "tv"), false, false); err != nil {
				return nil, err
			}
		}
	} else {
		if base.Has("imdb_id") || base.Has("film_name") {
			if err := s.search(ctx, with("type", "movie"), true, true); err != nil {
				return nil, err
			}
			if !p.currentPolicy().enabled {
				return nil, nil
			}
		}
		// Some movies have a wrong IMDb id or none: TMDB is the fallback.
		if len(s.items) == 0 && tmdb != "" {
			v := with("type", "movie", "tmdb_id", tmdb)
			v.Del("imdb_id")
			v.Del("film_name")
			primary := !base.Has("imdb_id") && !base.Has("film_name")
			before := len(s.items)
			if err := s.search(ctx, v, true, primary); err != nil {
				return nil, err
			}
			if len(s.items) > before {
				matchedID, matchedTitle = true, false
			}
		}
	}

	out := make([]subtitles.Candidate, 0, len(s.items))
	for _, it := range s.items {
		if c, ok := p.candidate(q, it, want, matchedID, matchedTitle); ok {
			out = append(out, c)
		}
	}
	return out, nil
}

// searchRun accumulates one Search's results across its requests, deduped
// on subtitlePage, else name (Bazarr's merge()).
type searchRun struct {
	p     *Provider
	items []item
	seen  map[string]bool
}

// search runs one search (Bazarr's _search), paging when paginate. Errors
// that describe the account or the service -- a bad key, the daily limit,
// a rate limit, an outage -- are returned so the caller throttles the
// provider. A rejection of this one request is absorbed: it must not
// discard what the other searches found, nor bench the provider. A
// transport failure is returned when primary (the provider is unreachable)
// and absorbed on a fallback search, as Bazarr absorbs both.
func (s *searchRun) search(ctx context.Context, params url.Values, paginate, primary bool) error {
	p := s.p
	pol := p.currentPolicy()
	if !pol.unpack {
		params.Del("unpack")
	}
	params.Set("subs_per_page", strconv.Itoa(subsPerPage))
	params.Set("api_key", p.cfg.APIKey)
	log := logging.FromContext(ctx)

	for page := 1; ; page++ {
		if page > 1 {
			params.Set("page", strconv.Itoa(page))
		}
		resp, err := p.get(ctx, p.cfg.Endpoint+"/subtitles?"+params.Encode())
		if err != nil {
			var pe *subtitles.ProviderError
			switch {
			case errors.Is(err, errNotFoundRoute):
				// A no-results search is a successful JSON response; a
				// bare 404 means the search route is not there at all.
				return &subtitles.ProviderError{Provider: p.Name(), Kind: subtitles.KindServiceUnavailable, Err: errors.New("search endpoint unavailable")}
			case errors.As(err, &pe), ctx.Err() != nil:
				return err
			case errors.Is(err, ErrRejected), !primary:
				log.Debug("subdl: search failed; continuing without it", "err", redact(err.Error()))
				return nil
			default:
				return fmt.Errorf("subtitles: subdl search: %w", err)
			}
		}
		raw, err := readCapped(resp.Body, maxJSONBytes)
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("subtitles: subdl search: %w", err)
		}
		var payload searchPayload
		if json.Unmarshal(raw, &payload) != nil {
			payload = searchPayload{} // Bazarr's _safe_json: an unparseable body is an empty one
		}
		if page == 1 {
			p.applyPolicy(payload.Policy)
			pol = p.currentPolicy()
		}
		if payload.isError() {
			log.Debug("subdl: search returned an error payload", "error", string(payload.Error))
			return nil
		}
		for _, it := range payload.Subtitles {
			key := it.SubtitlePage
			if key == "" {
				key = it.Name
			}
			if s.seen == nil {
				s.seen = map[string]bool{}
			}
			if s.seen[key] {
				continue
			}
			s.seen[key] = true
			s.items = append(s.items, it)
		}
		total := max(int(payload.TotalPages), 1)
		if !paginate || page >= total || page >= pol.maxPages || len(payload.Subtitles) < subsPerPage {
			return nil
		}
	}
}

func (p *Provider) currentPolicy() policy {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.policy
}

// applyPolicy is Bazarr's _apply_bazarr_policy: accept the bounded fields
// the server sends, clamping max_pages to [1, 2].
func (p *Provider) applyPolicy(pp *policyPayload) {
	if pp == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, f := range []struct {
		v   *bool
		dst *bool
	}{
		{pp.Enabled, &p.policy.enabled},
		{pp.SeasonFallbackEnabled, &p.policy.seasonFallback},
		{pp.TitleFallbackEnabled, &p.policy.titleFallback},
		{pp.UnpackEnabled, &p.policy.unpack},
	} {
		if f.v != nil {
			*f.dst = *f.v
		}
	}
	var n int
	if json.Unmarshal(pp.MaxPages, &n) == nil {
		p.policy.maxPages = min(max(n, 1), maxPages)
	}
}

// candidate turns one search result into a Candidate (the body of Bazarr's
// query() loop and SubdlSubtitle.get_matches), or reports that it is not
// one: a language not asked for, or a pack that does not hold the target
// episode.
func (p *Provider) candidate(q subtitles.Query, it item, want []wanted, matchedID, matchedTitle bool) (subtitles.Candidate, bool) {
	code := strings.ToUpper(strings.TrimSpace(it.Language))
	tag, ok := tagOf(code)
	if !ok {
		return subtitles.Candidate{}, false
	}
	hi, forced := it.hearingImpaired(), it.forced()
	accepted := false
	for _, w := range want {
		accepted = accepted || w.accepts(code, forced, hi)
	}
	if !accepted {
		return subtitles.Candidate{}, false
	}

	h := handle{Link: it.URL}
	id := it.Name
	season, ep := it.Season.positive(), it.Episode.positive()
	isPack := false
	episode := q.Kind == common.MediaKindEpisode
	if episode {
		from, end := it.EpisodeFrom.positive(), it.EpisodeEnd.positive()
		full := truthy(it.FullSeason)
		// Only when the API gave nothing: a single-episode row has
		// from == end, which a batch release name must not override.
		if from == 0 && end == 0 && !full {
			if f, e := episodeRangeFromReleases(it.Releases); f != 0 && e != 0 && f != e {
				from, end = f, e
			}
		}
		hasRange := from != 0 && end != 0 && from != end
		if hasRange && (q.Episode < from || q.Episode > end) {
			return subtitles.Candidate{}, false
		}
		if hasRange || full {
			isPack = true
			h.Pack, h.Season, h.Episode = true, q.Season, q.Episode
			// Prefer the member the server already extracted over
			// downloading the whole season archive.
			if f, ok := selectUnpackEntry(it.UnpackFiles, q.Episode, hi); ok {
				h = handle{Link: f.URL, Direct: true}
				id = it.Name + "/" + string(f.FileNID)
				if s := f.Season.positive(); s != 0 {
					season = s
				}
				ep, isPack = q.Episode, false
			}
		}
	}

	c := subtitles.Candidate{
		Provider: p.Name(), ID: id, FetchID: h.encode(), Language: tag,
		HI: hi, Forced: forced, ReleaseInfo: strings.Join(it.Releases, "\n"),
		Matches:      matches(q, it.Releases, matchedID, matchedTitle, season, ep, isPack),
		Uploader:     it.Author,
		AITranslated: truthy(it.AITranslated),
	}
	return c, true
}

// matches is SubdlSubtitle.get_matches in this package's Match keys. An id
// match becomes what Bazarr's score.py expands it to (series_imdb_id:
// series and year; imdb_id or tmdb_id: title and year); a film_name match
// is the series or title alone. The release names add what each of them
// corroborates about the target release (utils.update_matches).
func matches(q subtitles.Query, releases []string, matchedID, matchedTitle bool, season, episode int, isPack bool) map[string]bool {
	m := map[string]bool{}
	if q.Kind == common.MediaKindEpisode {
		switch {
		case matchedID:
			m[subtitles.MatchSeries], m[subtitles.MatchYear] = true, true
		case matchedTitle:
			m[subtitles.MatchSeries] = true
		}
		if episode == q.Episode || isPack {
			m[subtitles.MatchEpisode] = true
		}
		if season == q.Season {
			m[subtitles.MatchSeason] = true
		}
	} else {
		switch {
		case matchedID:
			m[subtitles.MatchTitle], m[subtitles.MatchYear] = true, true
		case matchedTitle:
			m[subtitles.MatchTitle] = true
		}
	}
	for _, r := range releases {
		for k, v := range subtitles.GuessMatches(q.Kind, q.Release, r) {
			m[k] = m[k] || v
		}
	}
	return m
}

// handle is what Download needs to fetch a candidate, carried opaquely in
// Candidate.FetchID: the link, and whether it is a bare file (a member the
// server unpacked) or an archive -- and for a season pack, which episode to
// take from it.
type handle struct {
	Link    string `json:"l"`
	Direct  bool   `json:"d,omitempty"`
	Pack    bool   `json:"p,omitempty"`
	Season  int    `json:"s,omitempty"`
	Episode int    `json:"e,omitempty"`
}

func (h handle) encode() string {
	b, _ := json.Marshal(h)
	return string(b)
}

// Download implements subtitles.Provider.Download (Bazarr's
// download_subtitle). A failure that is about this one candidate -- the
// link is gone, the archive holds no matching subtitle or cannot be read --
// is a plain error wrapping ErrRejected or ErrNoSubtitle, so the caller
// moves on to the next candidate without throttling the provider.
func (p *Provider) Download(ctx context.Context, c subtitles.Candidate) ([]byte, string, error) {
	ctx, span := tracing.Start(ctx, "subtitles.subdl.download")
	defer span.End()

	var h handle
	if err := json.Unmarshal([]byte(c.FetchID), &h); err != nil || h.Link == "" {
		return nil, "", fmt.Errorf("subtitles: subdl: invalid FetchID %q", c.FetchID)
	}
	link, err := p.resolveLink(h.Link)
	if err != nil {
		return nil, "", err
	}

	resp, err := p.get(ctx, link)
	if err != nil {
		tracing.RecordError(span, err)
		var pe *subtitles.ProviderError
		if errors.As(err, &pe) || errors.Is(err, ErrRejected) {
			return nil, "", err
		}
		return nil, "", fmt.Errorf("subtitles: subdl download: %w", err)
	}
	raw, err := readCapped(resp.Body, maxDownloadBytes)
	_ = resp.Body.Close()
	if err != nil {
		tracing.RecordError(span, err)
		return nil, "", fmt.Errorf("subtitles: subdl download: %w", err)
	}
	if len(raw) == 0 {
		return nil, "", fmt.Errorf("%w: empty body", ErrNoSubtitle)
	}

	a, err := subarchive.Open(raw, maxSubtitleBytes)
	switch {
	case errors.Is(err, subarchive.ErrNotArchive):
		if !h.Direct {
			return nil, "", fmt.Errorf("%w: the download is not an archive", ErrNoSubtitle)
		}
		if len(raw) > maxSubtitleBytes {
			return nil, "", fmt.Errorf("%w: at least %d bytes", ErrResponseTooLarge, len(raw))
		}
		return raw, path.Base(h.Link), nil
	case err != nil:
		// A corrupt archive is one candidate's defect (Bazarr absorbs it
		// for the same reason).
		return nil, "", fmt.Errorf("%w: %w", ErrNoSubtitle, err)
	}

	names := a.Names()
	var name string
	switch {
	case h.Pack:
		name, err = subarchive.Pick(names, h.Season, h.Episode, c.Forced)
	case len(names) > 0:
		name = names[0] // _first_subtitle_in_archive
	default:
		err = subarchive.ErrNoSubtitle
	}
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrNoSubtitle, err)
	}
	b, err := a.Read(name)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrNoSubtitle, err)
	}
	return b, path.Base(name), nil
}

// resolveLink joins a download link to the download endpoint. SubDL's
// links are relative paths; an absolute one is followed only when it
// points at the download host itself, so a search response cannot steer a
// download anywhere else.
func (p *Provider) resolveLink(link string) (string, error) {
	base, err := url.Parse(p.cfg.DownloadEndpoint + "/")
	if err != nil {
		return "", fmt.Errorf("subtitles: subdl: download endpoint: %w", err)
	}
	ref, err := url.Parse(link)
	if err != nil {
		return "", fmt.Errorf("%w: bad download link %q", ErrRejected, link)
	}
	if ref.IsAbs() && ref.Host != base.Host {
		return "", fmt.Errorf("%w: download link %q is not on %s", ErrRejected, link, base.Host)
	}
	return base.ResolveReference(ref).String(), nil
}

// imdbTT formats an IMDb id as SubDL takes it, "tt" and at least seven
// digits, from any of "tt0903747", "0903747" or "903747" (captionarr's
// fetch worker strips both the prefix and the leading zeros).
func imdbTT(id string) string {
	n := numeric(id)
	if n == "" {
		return ""
	}
	if len(n) < 7 {
		n = strings.Repeat("0", 7-len(n)) + n
	}
	return "tt" + n
}

// numeric strips an IMDb "tt" prefix and leading zeros, and returns "" for
// anything that is not then a positive decimal number.
func numeric(id string) string {
	s := strings.TrimLeft(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(id)), "tt"), "0")
	if s == "" {
		return ""
	}
	if _, err := strconv.ParseUint(s, 10, 64); err != nil {
		return ""
	}
	return s
}
