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

package cardigann

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mediactl/clustarr/pkg/newznab"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// Query is one search request, the local mirror of what the indexer
// controller derives from a catalog release-decision search (spec §5).
type Query struct {
	Type       string // "search"|"tv-search"|"movie-search"|"music-search"|"book-search"
	Q          string
	Categories []newznab.CategoryID

	IMDBID, TMDBID, TVDBID, TVMazeID, TraktID, DoubanID string
	Season, Ep                                          string
	Year                                                int
	Genre                                               string

	Album, Artist, Label, Track string
	Author, Title, Publisher    string
}

// canonicalFieldNames is FieldsBlock's own alternation (schema-v11.json),
// quoted verbatim. A field whose name is not in this set — including any
// name with an "_" suffix (title_optional, downloadvolumefactor_freeleech)
// — is an intermediate helper: it lands in TemplateContext.Result for later
// fields to read, but is never itself mapped onto a torznab.Release field.
// A "|append"/"|noappend" key is its base name plus a modifier; see
// splitFieldKey.
var canonicalFieldNames = map[string]bool{
	"download": true, "magnet": true, "infohash": true, "details": true, "comments": true,
	"title": true, "description": true, "category": true, "categorydesc": true, "size": true,
	"leechers": true, "seeders": true, "date": true, "files": true, "grabs": true,
	"downloadvolumefactor": true, "uploadvolumefactor": true, "minimumratio": true, "minimumseedtime": true,
	"imdb": true, "imdbid": true, "tmdbid": true, "rageid": true, "tvdbid": true, "tvmazeid": true,
	"traktid": true, "doubanid": true, "poster": true, "genre": true, "year": true, "author": true,
	"booktitle": true, "publisher": true, "album": true, "artist": true, "label": true, "track": true,
}

// implicitlyOptional are the fields Prowlarr treats as optional whether or
// not the definition says so (CardigannBase.OptionalFields): an id, a poster
// or a description a row lacks never costs the row.
var implicitlyOptional = map[string]bool{
	"imdb": true, "imdbid": true, "tmdbid": true, "rageid": true, "tvdbid": true, "tvmazeid": true,
	"traktid": true, "doubanid": true, "poster": true, "banner": true, "description": true, "genre": true,
}

// seasonEpisodeInQuery matches a query string that already looks like it
// carries its own season/episode marker (e.g. "Show Name S01E05"), so
// buildKeywords does not double up.
var seasonEpisodeInQuery = regexp.MustCompile(`(?i)\bS\d{1,2}E\d{1,3}\b`)

// buildKeywords derives .Keywords from a Query: q.Q, plus a zero-padded
// " S<season>E<episode>" suffix when q.Season/q.Ep are set and q.Q doesn't
// already look like a full release title — for definitions (like 1337x)
// whose search.paths templates only expose .Keywords, with no separate
// season/episode input field, so a season/episode search has to be folded
// into the search text itself.
func buildKeywords(q Query) string {
	kw := q.Q
	if q.Season == "" || q.Ep == "" {
		return kw
	}
	if seasonEpisodeInQuery.MatchString(kw) {
		return kw
	}
	season, errS := strconv.Atoi(q.Season)
	ep, errE := strconv.Atoi(q.Ep)
	if errS != nil || errE != nil {
		return kw
	}
	if kw != "" {
		kw += " "
	}
	return fmt.Sprintf("%sS%02dE%02d", kw, season, ep)
}

func yearString(y int) string {
	if y == 0 {
		return ""
	}
	return strconv.Itoa(y)
}

// queryVars projects a Query onto the smaller QueryVars shape templates
// see as .Query.*.
func queryVars(q Query, keywords string) QueryVars {
	return QueryVars{
		Type: q.Type, Q: q.Q, Keywords: keywords,
		IMDBID: q.IMDBID, IMDBIDShort: strings.TrimPrefix(q.IMDBID, "tt"),
		TVDBID: q.TVDBID, TMDBID: q.TMDBID, TVMazeID: q.TVMazeID,
		TraktID: q.TraktID, DoubanID: q.DoubanID,
		Season: q.Season, Ep: q.Ep, Episode: q.Ep,
		Year: yearString(q.Year), Genre: q.Genre,
		Album: q.Album, Artist: q.Artist, Label: q.Label, Track: q.Track,
		Author: q.Author, Title: q.Title, Publisher: q.Publisher,
	}
}

// searchPaths returns def.Search.Paths, or def.Search.Path wrapped as a
// single implicit entry with no Categories restriction when Paths is
// empty (the schema's oneOf guarantees exactly one of the two is set).
func searchPaths(def *Definition) []SearchPathBlock {
	if len(def.Search.Paths) > 0 {
		return def.Search.Paths
	}
	return []SearchPathBlock{{Path: def.Search.Path}}
}

// pathMatches reports whether p applies to the current request's tracker
// category ids (catStrings, from CategoryMapper.ToTracker). An empty
// p.Categories always matches — 1337x's own four paths declare no
// `categories:` field at all and therefore all four always fire
// regardless of the query, which is what the real file does, not a
// simplification. A "!id" entry excludes.
func pathMatches(p SearchPathBlock, catStrings []string) bool {
	if len(p.Categories) == 0 {
		return true
	}
	have := make(map[string]bool, len(catStrings))
	for _, c := range catStrings {
		have[c] = true
	}
	var positive []string
	for _, c := range p.Categories {
		s := string(c)
		if strings.HasPrefix(s, "!") {
			if have[strings.TrimPrefix(s, "!")] {
				return false
			}
			continue
		}
		positive = append(positive, s)
	}
	if len(positive) == 0 {
		return true
	}
	for _, p := range positive {
		if have[p] {
			return true
		}
	}
	return false
}

// Search executes every SearchBlock.Paths entry whose Categories intersect
// query.Categories (via the CategoryMapper; a path with no Categories
// always matches; "!id" excludes), or the single SearchBlock.Path when Paths
// is empty, builds .Keywords from Query, applies KeywordsFilters, sends
// each request (GET query string or POST form, per path.Method, in the
// definition's encoding), parses the response per path.Response.Type
// (default HTML) after PreprocessingFilters, iterates Rows (After merges,
// Multiple expands), evaluates Fields in file order into a per-row Result,
// and maps the canonical field names (note §3.8) onto a torznab.Release,
// falling back to rows.dateheaders for a row with no date. A non-optional
// field with no match drops that row (logged at Debug via
// logging.FromContext, not returned as an error) rather than failing the
// whole search.
//
// A response that is not a result page is an error, never zero results: a
// redirect the path did not ask to follow (*RedirectError; to the login
// page it matches ErrSessionExpired), a non-2xx status (*StatusError), or a
// matched search.error block (*SearchError).
func (e Engine) Search(ctx context.Context, def *Definition, cfg Config, query Query) ([]torznab.Release, error) {
	if loginRequiresSession(def.Login) && cfg.Session == nil {
		return nil, ErrSessionRequired
	}

	tc := e.templateContext(def, cfg)
	keywords := buildKeywords(query)
	tc.Keywords = keywords
	tc.Query = queryVars(query, keywords)

	mapper := NewCategoryMapper(def)
	tc.Categories = mapper.ToTracker(query.Categories)

	for _, f := range def.Search.KeywordsFilters {
		fn, ok := Filters[f.Name]
		if !ok {
			return nil, fmt.Errorf("cardigann: unknown keywords filter %q", f.Name)
		}
		kw, err := fn(ctx, tc.Keywords, []string(f.Args), tc)
		if err != nil {
			return nil, fmt.Errorf("cardigann: keywords filter %q: %w", f.Name, err)
		}
		tc.Keywords = kw
		tc.Query.Keywords = kw
	}

	var releases []torznab.Release
	for _, p := range searchPaths(def) {
		if !pathMatches(p, tc.Categories) {
			continue
		}
		rels, err := e.searchOnePath(ctx, def, cfg, tc, p, mapper)
		if err != nil {
			return nil, err
		}
		releases = append(releases, rels...)
	}
	return releases, nil
}

// searchOnePath builds and sends the request for one SearchPathBlock,
// parses the response and maps every surviving row onto a torznab.Release.
func (e Engine) searchOnePath(ctx context.Context, def *Definition, cfg Config, tc *TemplateContext, p SearchPathBlock, mapper CategoryMapper) ([]torznab.Release, error) {
	req, err := e.buildSearchRequest(ctx, cfg, tc, &def.Search, p)
	if err != nil {
		return nil, err
	}
	resp, body, err := e.do(ctx, req, exchange{def: def, site: cfg.BaseURL, follow: p.FollowRedirect})
	if err != nil {
		return nil, err
	}
	if err := checkSearchResponse(def, cfg, req, resp); err != nil {
		return nil, err
	}

	rt := responseType(p)
	if body, err = decodeBody(tc.enc, body); err != nil {
		return nil, err
	}
	if noResults(p, rt, body) {
		return nil, nil
	}
	if body, err = preprocess(ctx, body, rt, def.Search.PreprocessingFilters, tc); err != nil {
		return nil, err
	}
	doc, err := ParseDoc(rt, body)
	if err != nil {
		return nil, err
	}

	// search.error BEFORE rows. A tracker's error or rate-limit page is a
	// well-formed document with no result rows in it, so without this the
	// row extraction below returns zero releases and the caller reads a
	// failing tracker as a tracker with nothing to offer -- which never
	// trips indexarr's health escalation or backoff (Phase G ruling R6).
	if err := checkSearchErrors(ctx, doc, def.Search.Error, tc); err != nil {
		return nil, err
	}

	rows, err := extractRows(ctx, doc, def.Search.Rows, tc)
	if err != nil {
		return nil, err
	}

	var out []torznab.Release
	for _, row := range rows {
		rr, ok, err := evaluateRow(ctx, def.Search.Fields, tc, row, mapper)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		rel := mapResultToRelease(def, cfg, rr, tc)
		if rel.PubDate.IsZero() && def.Search.Rows.DateHeaders != nil {
			if !applyDateHeader(ctx, &rel, row.doc, *def.Search.Rows.DateHeaders, tc) {
				continue
			}
		}
		out = append(out, rel)
	}
	return out, nil
}

// responseType maps a path's response.type onto the selector backend.
func responseType(p SearchPathBlock) ResponseType {
	if p.Response != nil {
		switch p.Response.Type {
		case "json":
			return ResponseJSON
		case "xml":
			return ResponseXML
		}
	}
	return ResponseHTML
}

// ErrRedirected is what every *RedirectError matches.
var ErrRedirected = errors.New("cardigann: the indexer redirected the search")

// ErrSessionExpired is what a *RedirectError to the tracker's login page
// also matches: the session was killed or expired, and the caller should log
// in again rather than count the indexer as failing.
var ErrSessionExpired = errors.New("cardigann: the indexer redirected to its login page")

// RedirectError is a search response that redirected when its path did not
// set followredirect. Prowlarr's CardigannParser throws on exactly this
// (HasHttpRedirect), and for the same reason: the page at the other end is
// a login form or a landing page, and parsing it as a result page reads a
// dead session as a search that found nothing.
type RedirectError struct {
	// Location is the redirect target with its query string, fragment
	// and userinfo removed (RedactURL); a tracker's redirect can carry a
	// passkey.
	Location string
	// LoginPage reports that the target is the login page.
	LoginPage bool
}

func (e *RedirectError) Error() string {
	if e.LoginPage {
		return "cardigann: redirected to the login page (" + e.Location + "); the session has expired"
	}
	return "cardigann: redirected to " + e.Location
}

// Unwrap makes errors.Is(err, ErrRedirected) hold, and
// errors.Is(err, ErrSessionExpired) for a redirect to the login page.
func (e *RedirectError) Unwrap() []error {
	if e.LoginPage {
		return []error{ErrRedirected, ErrSessionExpired}
	}
	return []error{ErrRedirected}
}

// ErrUnexpectedStatus is what every *StatusError matches.
var ErrUnexpectedStatus = errors.New("cardigann: unexpected HTTP status")

// StatusError is a search response with a non-2xx status. Prowlarr's parser
// refuses anything but 200 ("Unexpected response status"); a 500 page parsed
// as HTML has no rows, which is the failing-tracker-as-empty-tracker
// confusion search.error exists to prevent.
type StatusError struct{ StatusCode int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("cardigann: unexpected response status %d", e.StatusCode)
}

// Unwrap makes errors.Is(err, ErrUnexpectedStatus) hold.
func (e *StatusError) Unwrap() error { return ErrUnexpectedStatus }

// checkSearchResponse turns a redirect or a non-2xx status into an error.
func checkSearchResponse(def *Definition, cfg Config, req *http.Request, resp *http.Response) error {
	if resp == nil {
		return nil
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if loc := resp.Header.Get("Location"); loc != "" {
			target, err := req.URL.Parse(loc)
			if err != nil {
				return &RedirectError{Location: redactRawURL(loc)}
			}
			return &RedirectError{Location: RedactURL(target), LoginPage: isLoginPage(def, cfg, target)}
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &StatusError{StatusCode: resp.StatusCode}
	}
	return nil
}

// isLoginPage reports whether target is the tracker's login page: Prowlarr's
// test (the target contains "/login.php", case-insensitively), or the
// definition's own login.path, when that is a plain path rather than a
// template.
func isLoginPage(def *Definition, cfg Config, target *url.URL) bool {
	if strings.Contains(strings.ToLower(target.Path), "/login.php") {
		return true
	}
	if def.Login == nil || def.Login.Path == "" || strings.Contains(def.Login.Path, "{{") {
		return false
	}
	login, err := resolveURL(cfg.BaseURL, def.Login.Path)
	if err != nil {
		return false
	}
	lu, err := url.Parse(login)
	if err != nil {
		return false
	}
	return strings.EqualFold(lu.Host, target.Host) && strings.TrimSuffix(lu.Path, "/") == strings.TrimSuffix(target.Path, "/")
}

// noResults reports whether a JSON response is a tracker's "nothing found"
// text instead of JSON: it contains the path's response.noResultsMessage,
// or -- when that message is declared empty -- the body is blank. Such a
// body is not JSON at all, so without this the search fails to parse rather
// than returning zero results. Prowlarr's CardigannParser checks it before
// parsing JSON, and only for JSON.
func noResults(p SearchPathBlock, rt ResponseType, body []byte) bool {
	if rt != ResponseJSON || p.Response == nil || p.Response.NoResultsMessage == nil {
		return false
	}
	msg := *p.Response.NoResultsMessage
	if strings.TrimSpace(msg) == "" {
		return len(bytes.TrimSpace(body)) == 0
	}
	return bytes.Contains(body, []byte(msg))
}

// preprocess runs search.preprocessingfilters over the raw response text
// before it is parsed -- a tracker whose markup a selector cannot reach
// until a re_replace has fixed it. Prowlarr applies them to HTML and XML
// responses and not to JSON, and so does this.
func preprocess(ctx context.Context, body []byte, rt ResponseType, filters []FilterBlock, tc *TemplateContext) ([]byte, error) {
	if rt == ResponseJSON || len(filters) == 0 {
		return body, nil
	}
	text := string(body)
	for _, f := range filters {
		fn, ok := Filters[f.Name]
		if !ok {
			return nil, fmt.Errorf("cardigann: unknown preprocessing filter %q", f.Name)
		}
		var err error
		text, err = fn(ctx, text, []string(f.Args), tc)
		if err != nil {
			return nil, fmt.Errorf("cardigann: preprocessing filter %q: %w", f.Name, err)
		}
	}
	return []byte(text), nil
}

// applyDateHeader is rows.dateheaders: for a row whose fields yielded no
// date, walk back through the preceding rows -- each previous element
// sibling, then the parent's previous sibling when a row is first in its
// parent -- and take the first on which the dateheaders selector matches
// (the row itself or a descendant, as Prowlarr's HandleSelector matches).
// It reports whether the row survives: a non-optional dateheaders that finds
// no header, or finds one that does not parse as a date, drops the row, as
// Prowlarr's per-row exception does.
func applyDateHeader(ctx context.Context, rel *torznab.Release, row Doc, b SelectorBlock, tc *TemplateContext) bool {
	log := logging.FromContext(ctx)
	val, found := findDateHeader(ctx, row, b, tc)
	if !found {
		if !b.Optional {
			log.Debug("cardigann: dropping row: no date header found")
			return false
		}
		return true
	}
	t, err := parseUnknownDate(val, tc.effectiveNow())
	if err != nil {
		log.Debug("cardigann: dropping row: date header does not parse", "error", err)
		return false
	}
	rel.PubDate = t
	return true
}

// findDateHeader walks back from row looking for the dateheaders selector.
// Every failure on a candidate row -- no match, a filter that rejects the
// text -- moves on to the one before it, as Prowlarr's loop swallows the
// exception and continues. Default/optional do not apply per candidate;
// they decide only what happens when no row matches.
func findDateHeader(ctx context.Context, row Doc, b SelectorBlock, tc *TemplateContext) (string, bool) {
	b.Default, b.Optional = nil, false
	for prev, ok := row.prevRow(); ok; prev, ok = prev.prevRow() {
		cand := b
		if cand.Text == nil && cand.Selector != "" {
			sel, err := render(cand.Selector, tc)
			if err != nil {
				return "", false
			}
			if prev.matches(sel) {
				cand.Selector = ""
			}
		}
		val, found, err := cand.Extract(ctx, prev, tc)
		if err == nil && found {
			return val, true
		}
	}
	return "", false
}

// unknownDateLayouts are what parseUnknownDate tries after RFC 3339 (the
// shape every date filter in this package emits) and a Unix timestamp.
var unknownDateLayouts = []string{
	time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822,
	"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02",
}

// parseUnknownDate is a subset of Prowlarr's DateTimeUtil.FromUnknown: RFC
// 3339, a Unix timestamp in seconds (or milliseconds, at 13 digits), the
// RFC 1123/822 forms and bare ISO date-times (read as UTC), plus the
// relative forms the fuzzytime and timeago filters accept. A definition
// with a stranger date runs a dateparse filter first, which emits RFC 3339.
func parseUnknownDate(v string, now time.Time) (time.Time, error) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
		if len(v) >= 13 {
			return time.UnixMilli(n).UTC(), nil
		}
		return time.Unix(n, 0).UTC(), nil
	}
	for _, layout := range unknownDateLayouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t, nil
		}
	}
	if d, err := parseRelativeDuration(v); err == nil {
		return now.Add(-d), nil
	}
	tc := &TemplateContext{Now: now}
	if s, err := filterFuzzytime(context.Background(), v, nil, tc); err == nil {
		return time.Parse(time.RFC3339, s)
	}
	return time.Time{}, fmt.Errorf("cardigann: unrecognised date %q", v)
}

// ErrSearchFailed is the sentinel every *SearchError unwraps to, so a caller
// can test errors.Is(err, cardigann.ErrSearchFailed) through any wrapping.
var ErrSearchFailed = errors.New("cardigann: the indexer reported a search error")

// SearchError is a search.error block that matched the response: the
// tracker answered, and what it answered with was an error page (a
// rate-limit notice, a banned account, an expired API key) rather than
// results.
//
// Message is the TRACKER'S text. It is diagnostic, bounded by
// maxSearchErrorMessage, and must never become a metric label.
type SearchError struct{ Message string }

func (e *SearchError) Error() string { return "cardigann: search failed: " + e.Message }

// Unwrap makes errors.Is(err, ErrSearchFailed) hold.
func (e *SearchError) Unwrap() error { return ErrSearchFailed }

// maxSearchErrorMessage bounds SearchError.Message. An error selector that
// matches `:root` (0dayfiles-api.yml's "Account is Banned" check does
// exactly that) would otherwise carry the whole page into the error.
const maxSearchErrorMessage = 512

// checkSearchErrors evaluates search.error against one parsed response.
//
// It follows Prowlarr's CardigannBase.CheckForError: the first block whose
// Selector matches wins; the message is the block's Message selector when it
// has one -- evaluated against the whole document, as Prowlarr does, so a
// `text:` template or a selector elsewhere on the page both work -- and
// otherwise the matched element's own text. ErrorBlock.Path is not
// consulted: it is a login-only discriminator (which page of a multi-step
// login the block applies to) and a search has exactly one response.
func checkSearchErrors(ctx context.Context, doc Doc, errs []ErrorBlock, tc *TemplateContext) error {
	for _, eb := range errs {
		if eb.Selector == "" {
			continue
		}
		matched, ok := doc.Select(eb.Selector)
		if !ok {
			continue
		}
		msg := ""
		if eb.Message != nil {
			rendered, mok, merr := eb.Message.Extract(ctx, doc, tc)
			if merr != nil {
				return fmt.Errorf("cardigann: search error message: %w", merr)
			}
			if mok {
				msg = rendered
			}
		}
		if msg == "" {
			msg, _ = matched.Text("")
		}
		msg = strings.Join(strings.Fields(msg), " ")
		if msg == "" {
			msg = "the indexer returned an error page"
		}
		return &SearchError{Message: truncateRunes(msg, maxSearchErrorMessage)}
	}
	return nil
}

// truncateRunes shortens s to at most n bytes without splitting a UTF-8
// sequence.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// buildSearchRequest renders p.Path and every input (search.inputs merged
// with path.inputs, path winning) as templates, then builds a GET (query
// string) or POST (form body) request per p.Method (default GET).
// Empty-valued inputs are omitted unless AllowEmptyInputs.
//
// The path and "$raw" are rendered as Prowlarr's GetRequest and Jackett's
// PerformQuery render them (identical in both):
//
//   - The path with every substitution URL-encoded (WebUtility.UrlEncode)
//     and then every "+" made "%20" ("HttpUtility.UrlPathEncode seems to
//     only encode spaces, we use UrlEncode and replace + with %20"), so a
//     keyword's "/", "?", "#" or "&" cannot restructure the URL. Until gap
//     fix Z6 the keywords went in raw: "AC/DC" added a path segment and a
//     "?" in a title started the query string.
//   - "$raw" with its substitutions URL-encoded the same way, split on "&"
//     into key=value pairs (an empty key dropped, the value after the first
//     "="), and each pair added to the inputs, whose values are encoded
//     again when the query is -- Prowlarr's `queryCollection.Add(key,
//     value)` then GetQueryString. It was appended verbatim, so a raw
//     keyword's space or "&" reached the wire unencoded.
//
// A GET keeps whatever query the path already carries and appends the
// inputs after it. Until gap fix Z6 the inputs replaced it, and with no
// inputs an empty query did: the 47 bundled search paths that carry their
// whole query in the path ("search.php?q={{ .Keywords }}") searched with
// none. (Prowlarr appends "?" and the inputs whatever the path holds, which
// for the four bundled paths with both yields a second "?"; this joins
// with "&" instead.)
func (e Engine) buildSearchRequest(ctx context.Context, cfg Config, tc *TemplateContext, sb *SearchBlock, p SearchPathBlock) (*http.Request, error) {
	renderedPath, err := renderModified(p.Path, tc, webURLEncode)
	if err != nil {
		return nil, err
	}
	renderedPath = strings.ReplaceAll(renderedPath, "+", "%20")

	inputs := make(map[string]Scalar, len(sb.Inputs)+len(p.Inputs))
	if p.InheritInputs == nil || *p.InheritInputs {
		for k, v := range sb.Inputs {
			inputs[k] = v
		}
	}
	for k, v := range p.Inputs { // path wins
		inputs[k] = v
	}

	values := url.Values{}
	if raw, ok := inputs["$raw"]; ok {
		rendered, err := renderModified(string(raw), tc, webURLEncode)
		if err != nil {
			return nil, err
		}
		for _, part := range strings.Split(rendered, "&") {
			key, value, _ := strings.Cut(part, "=")
			if key == "" {
				continue
			}
			values.Add(key, value)
		}
	}
	for k, v := range inputs {
		if k == "$raw" {
			continue
		}
		rendered, err := render(string(v), tc)
		if err != nil {
			return nil, err
		}
		if rendered == "" && !sb.AllowEmptyInputs {
			continue
		}
		values.Set(k, rendered)
	}

	method := strings.ToUpper(p.Method)
	if method == "" {
		method = http.MethodGet
	}

	u, err := resolveURL(cfg.BaseURL, renderedPath)
	if err != nil {
		return nil, err
	}

	var req *http.Request
	if method == http.MethodGet {
		parsed, err := url.Parse(u)
		if err != nil {
			return nil, fmt.Errorf("cardigann: search url %q: %w", redactRawURL(u), RedactErr(err))
		}
		parsed.RawQuery = joinQuery(parsed.RawQuery, encodeValues(values, tc.enc, p.QuerySeparator))
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("cardigann: build request: %w", RedactErr(err))
		}
	} else {
		body := encodeValues(values, tc.enc, "")
		req, err = http.NewRequestWithContext(ctx, method, u, strings.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("cardigann: build request: %w", RedactErr(err))
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	if err := renderHeaders(req, sb.Headers, tc); err != nil {
		return nil, err
	}
	attachSession(req, cfg.Session)
	return req, nil
}

// joinQuery appends query to the query a path already carries.
func joinQuery(existing, query string) string {
	switch {
	case existing == "":
		return query
	case query == "":
		return existing
	default:
		return existing + "&" + query
	}
}

// searchRow is one result row: the Doc its fields evaluate against, and --
// for JSON -- the row as rows.selector found it, before rows.attribute
// descended into it. A JSON field selector starting ".." reads from parent
// (Prowlarr's HandleJsonSelector trims the dots; CardigannParser switches
// the parent object in), which is how a rows.multiple definition reads the
// movie a torrent belongs to.
type searchRow struct {
	doc    Doc
	parent Doc
}

// extractRows locates each result row within doc per rows.Selector (and,
// for JSON bodies, one further Attribute descent — e.g. 0dayfiles-api.yml's
// `attribute: attributes`). rows.Count, when set, short-circuits to zero
// rows when it resolves to "0" or empty. rows.after merges each row's
// following rows into it (HTML/XML); rows.multiple makes each element of a
// JSON row's attribute value its own row.
func extractRows(ctx context.Context, doc Doc, rows RowsBlock, tc *TemplateContext) ([]searchRow, error) {
	if rows.Count != nil {
		val, ok, err := rows.Count.Extract(ctx, doc, tc)
		if err != nil {
			return nil, err
		}
		if !ok || val == "" || val == "0" {
			return nil, nil
		}
	}

	// rows.Selector is itself a template (1337x's own rows.selector
	// conditionally appends an :has(...:contains(...)) clause driven by
	// .Config.uploader) — render before handing it to Doc.Rows.
	renderedSelector, err := render(rows.Selector, tc)
	if err != nil {
		return nil, err
	}
	base := doc.Rows(renderedSelector)
	if doc.rt != ResponseJSON {
		base = mergeFollowingRows(base, rows.After)
	}
	out := make([]searchRow, 0, len(base))
	for _, r := range base {
		sub := r
		if rows.Attribute != "" {
			var ok bool
			sub, ok = r.Select(rows.Attribute)
			if !ok {
				if rows.MissingAttributeEqualsNoResults {
					continue
				}
				return nil, fmt.Errorf("cardigann: row missing attribute %q", rows.Attribute)
			}
		}
		if rows.Multiple && sub.rt == ResponseJSON && sub.json.IsArray() {
			for _, el := range sub.elements() {
				out = append(out, searchRow{doc: el, parent: r})
			}
			continue
		}
		out = append(out, searchRow{doc: sub, parent: r})
	}
	return out, nil
}

// mergeFollowingRows is rows.after, Prowlarr's merge verbatim except at the
// end of the table: each row absorbs the child nodes of the after rows that
// follow it, and those rows leave the list. Prowlarr indexes past the end
// when the last row has fewer than after followers, failing the whole
// search; this merges what is there.
func mergeFollowingRows(rows []Doc, after int) []Doc {
	if after <= 0 {
		return rows
	}
	out := make([]Doc, 0, len(rows)/(after+1)+1)
	for i := 0; i < len(rows); i += after + 1 {
		cur := rows[i]
		for j := 1; j <= after && i+j < len(rows); j++ {
			cur.absorb(rows[i+j])
		}
		out = append(out, cur)
	}
	return out
}

// rowResult is one evaluated row: the .Result values by field name, and the
// categories the category/categorydesc fields mapped to, in the order the
// fields ran (the |append/|noappend modifiers make that order matter).
type rowResult struct {
	values map[string]string
	cats   []newznab.CategoryID
}

// splitFieldKey separates a search.fields key into its base name and its
// modifiers: "title|append" is ("title", ["append"]). The schema admits
// |append on title and description and |append/|noappend on category and
// categorydesc (FieldsBlock's patternProperties).
func splitFieldKey(key string) (string, []string) {
	parts := strings.Split(key, "|")
	return parts[0], parts[1:]
}

func hasModifier(mods []string, m string) bool {
	for _, x := range mods {
		if x == m {
			return true
		}
	}
	return false
}

// evaluateRow walks fields in file order, extracting each into the row's
// result; tc.Result is set to the same map so later fields can read earlier
// ones via .Result.<name> (1337x's title_optional/title_default/title
// chain). A required canonical field name (note §3.8) whose Extract misses
// (ok == false) drops the row. An optional one does not -- declared
// `optional: true`, or one of Prowlarr's implicitlyOptional names -- and
// until Task X8a it did, so a definition's optional date or imdbid dropped
// every row that lacked one. A non-canonical/intermediate name is simply
// left unset, and so is a modified key -- "title|append" with nothing to
// append leaves the title the plain field set.
//
// Modifiers follow Prowlarr's CardigannParser.ParseFields: title|append and
// description|append concatenate onto the value so far (and .Result.title
// is the concatenation); a category or categorydesc field unions its mapped
// categories into the row's, unless it is |noappend, which replaces them.
func evaluateRow(ctx context.Context, fields OrderedFields, tc *TemplateContext, row searchRow, mapper CategoryMapper) (rowResult, bool, error) {
	rr := rowResult{values: make(map[string]string, len(fields))}
	tc.Result = rr.values
	catsSet := false
	for _, entry := range fields {
		name, mods := splitFieldKey(entry.Name)
		src, block := row.doc, entry.Block
		if src.rt == ResponseJSON && strings.HasPrefix(block.Selector, "..") {
			src = row.parent
		}
		val, ok, err := block.Extract(ctx, src, tc)
		if err != nil {
			return rowResult{}, false, fmt.Errorf("cardigann: field %q: %w", entry.Name, err)
		}
		if !ok {
			if len(mods) == 0 && canonicalFieldNames[name] && !block.Optional && !implicitlyOptional[name] {
				logging.FromContext(ctx).Debug("cardigann: dropping row: required field missing", "field", entry.Name)
				return rowResult{}, false, nil
			}
			continue
		}
		switch name {
		case "title", "description":
			if hasModifier(mods, "append") {
				val = rr.values[name] + val
			}
		case "category", "categorydesc":
			var ids []newznab.CategoryID
			if name == "category" {
				ids = mapper.FromTracker(val)
			} else {
				ids = mapper.FromTrackerDesc(val)
			}
			if len(ids) > 0 {
				if !catsSet || hasModifier(mods, "noappend") {
					rr.cats = ids
				} else {
					rr.cats = unionCategories(rr.cats, ids)
				}
				catsSet = true
			}
		}
		rr.values[name] = val
	}
	return rr, true, nil
}

// unionCategories appends the ids in add that have not already appeared.
func unionCategories(have, add []newznab.CategoryID) []newznab.CategoryID {
	seen := make(map[newznab.CategoryID]bool, len(have)+len(add))
	out := make([]newznab.CategoryID, 0, len(have)+len(add))
	for _, id := range append(append([]newznab.CategoryID(nil), have...), add...) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// firstNonEmpty returns the first non-empty string among vs.
func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

var imdbTTRe = regexp.MustCompile(`^(?i)tt\d+$`)

// normalizeIMDB accepts either a bare-digit or "tt"-prefixed imdb id and
// returns the canonical "tt" + 7-digit form, matching
// commonv1alpha1.IDKeyIMDB's convention (torznab.Release.IDs' own doc
// comment: "The IMDb value carries the canonical tt prefix").
func normalizeIMDB(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	digits := v
	if imdbTTRe.MatchString(v) {
		digits = v[2:]
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return v
	}
	return fmt.Sprintf("tt%07d", n)
}

// splitGenre splits a genre field's value on "," or "|" — the corpus's own
// separators — into torznab.Release.Attrs["genre"]'s slice form.
func splitGenre(v string) []string {
	fields := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == '|' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// declaredFields reports whether fields declares name at all (regardless
// of whether it matched on a given row) — the "entirely absent from the
// definition" test downloadvolumefactor/uploadvolumefactor's 1.0 default
// depends on, as opposed to merely being empty for one row.
func declaredField(fields OrderedFields, name string) bool {
	for _, f := range fields {
		if f.Name == name {
			return true
		}
	}
	return false
}

// mapResultToRelease maps one row's canonical Result values (note §3.8)
// onto a torznab.Release. torznab.Release has no music/book fields (unlike
// the research note's older §11 sketch — confirmed against B6's actual
// struct): year/author/booktitle/publisher/artist/album/label/track all
// land in Attrs instead; a later phase may extend torznab.Release itself.
func mapResultToRelease(def *Definition, cfg Config, rr rowResult, tc *TemplateContext) torznab.Release {
	result := rr.values
	rel := torznab.Release{
		Attrs: map[string][]string{},
		IDs:   map[string]string{},
	}

	rel.Title = result["title"]
	rel.Description = result["description"]
	rel.GUID = firstNonEmpty(result["details"], result["download"], result["magnet"])

	if v := result["details"]; v != "" {
		if abs, err := resolveURL(cfg.BaseURL, v); err == nil {
			rel.CommentURL = abs
		}
	}
	if v := result["download"]; v != "" {
		if strings.HasPrefix(v, "magnet:") {
			rel.MagnetURL = v
		} else if abs, err := resolveURL(cfg.BaseURL, v); err == nil {
			rel.Link = abs
		} else {
			rel.Link = v
		}
	}
	if v := result["magnet"]; v != "" {
		rel.MagnetURL = v
	}
	if v := result["infohash"]; v != "" {
		rel.InfoHash = v
	}

	if v := result["size"]; v != "" {
		if n, err := GetBytes(v); err == nil {
			rel.Size = n
		}
	}

	var seeders, leechers *int32
	if v, ok := result["seeders"]; ok {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil {
			nn := int32(n)
			seeders = &nn
		}
	}
	if v, ok := result["leechers"]; ok {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil {
			nn := int32(n)
			leechers = &nn
		}
	}
	rel.Seeders, rel.Leechers = seeders, leechers
	if seeders != nil && leechers != nil {
		sum := *seeders + *leechers
		rel.Peers = &sum
	}
	if v, ok := result["grabs"]; ok {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil {
			nn := int32(n)
			rel.Grabs = &nn
		}
	}
	if v, ok := result["files"]; ok {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil {
			nn := int32(n)
			rel.Files = &nn
		}
	}

	if v := result["date"]; v != "" {
		if t, err := parseUnknownDate(v, tc.effectiveNow()); err == nil {
			rel.PubDate = t
		}
	}

	rel.Categories = rr.cats

	rel.DownloadVolumeFactor = ratioField(result, def.Search.Fields, "downloadvolumefactor")
	rel.UploadVolumeFactor = ratioField(result, def.Search.Fields, "uploadvolumefactor")
	rel.MinimumRatio = ratioField(result, def.Search.Fields, "minimumratio")
	if v, ok := result["minimumseedtime"]; ok && v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			seconds := int64(n)
			rel.MinimumSeedTime = &seconds
		}
	}

	if v := firstNonEmpty(result["imdbid"], result["imdb"]); v != "" {
		rel.IDs["imdb"] = normalizeIMDB(v)
	}
	for _, name := range []string{"tmdbid", "tvdbid", "rageid", "tvmazeid", "traktid", "doubanid"} {
		if v := result[name]; v != "" {
			rel.IDs[name] = v
		}
	}

	rel.Poster = result["poster"]
	if v := result["genre"]; v != "" {
		rel.Attrs["genre"] = splitGenre(v)
	}
	for _, name := range []string{"year", "author", "booktitle", "publisher", "artist", "album", "label", "track"} {
		if v := result[name]; v != "" {
			rel.Attrs[name] = []string{v}
		}
	}

	return rel
}

// ratioField reads a *float64 field (downloadvolumefactor,
// uploadvolumefactor, minimumratio): the row's own value when present and
// parseable, else 1.0 when the field is entirely absent from the
// definition (not merely empty for this one row), else nil.
func ratioField(result map[string]string, fields OrderedFields, name string) *float64 {
	if v, ok := result[name]; ok && v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return &f
		}
		return nil
	}
	if !declaredField(fields, name) {
		one := 1.0
		return &one
	}
	return nil
}
