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

// ErrUnsupportedRowFeature is returned when a Definition's search.rows uses
// After, DateHeaders or the FieldsBlock `|append`/`|noappend` key form —
// decoded (Load/Validate succeed) but not implemented in the row-extraction
// engine. Neither bundled definition (1337x, 0dayfiles-api) uses them;
// implementing them correctly needs a fixture this task doesn't have. A
// later task adds real coverage before lifting the gap.
var ErrUnsupportedRowFeature = errors.New("cardigann: rows.after/dateheaders or a field's |append modifier is not yet supported")

// canonicalFieldNames is FieldsBlock's own alternation (schema-v11.json),
// quoted verbatim. A field whose name is not in this set — including any
// name with an "_" suffix (title_optional, downloadvolumefactor_freeleech)
// or a "|append"/"|noappend" form — is an intermediate helper: it lands in
// TemplateContext.Result for later fields to read, but is never itself
// mapped onto a torznab.Release field.
var canonicalFieldNames = map[string]bool{
	"download": true, "magnet": true, "infohash": true, "details": true, "comments": true,
	"title": true, "description": true, "category": true, "categorydesc": true, "size": true,
	"leechers": true, "seeders": true, "date": true, "files": true, "grabs": true,
	"downloadvolumefactor": true, "uploadvolumefactor": true, "minimumratio": true, "minimumseedtime": true,
	"imdb": true, "imdbid": true, "tmdbid": true, "rageid": true, "tvdbid": true, "tvmazeid": true,
	"traktid": true, "doubanid": true, "poster": true, "genre": true, "year": true, "author": true,
	"booktitle": true, "publisher": true, "album": true, "artist": true, "label": true, "track": true,
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
// each request (GET query string or POST form, per path.Method), parses the
// response per path.Response.Type (default HTML), iterates Rows (After == 0
// and DateHeaders == nil, else ErrUnsupportedRowFeature), evaluates Fields
// in file order into a per-row Result, and maps the canonical field names
// (note §3.8) onto a torznab.Release. A non-optional field with no match
// drops that row (logged at Debug via logging.FromContext, not returned as
// an error) rather than failing the whole search.
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
	_, body, err := e.do(ctx, req)
	if err != nil {
		return nil, err
	}

	rt := ResponseHTML
	if p.Response != nil {
		switch p.Response.Type {
		case "json":
			rt = ResponseJSON
		case "xml":
			rt = ResponseXML
		}
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
		result, ok, err := evaluateRow(ctx, def.Search.Fields, tc, row)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		rel := mapResultToRelease(def, cfg, result, mapper)
		out = append(out, rel)
	}
	return out, nil
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
// "$raw" is rendered once and appended verbatim after every ordinary
// key=value pair, not url.Values-encoded — 0dayfiles-api.yml's
// `$raw: "{{ range .Categories }}&categories[]={{.}}{{end}}"` relies on
// this. Empty-valued inputs are omitted unless AllowEmptyInputs.
func (e Engine) buildSearchRequest(ctx context.Context, cfg Config, tc *TemplateContext, sb *SearchBlock, p SearchPathBlock) (*http.Request, error) {
	renderedPath, err := render(p.Path, tc)
	if err != nil {
		return nil, err
	}

	inputs := make(map[string]Scalar, len(sb.Inputs)+len(p.Inputs))
	for k, v := range sb.Inputs {
		inputs[k] = v
	}
	for k, v := range p.Inputs { // path wins; see the TODO(inheritinputs) note below
		inputs[k] = v
	}
	// TODO(inheritinputs): SearchPathBlock.InheritInputs is decoded but
	// given no behaviour distinct from this always-merge default; neither
	// bundled definition disambiguates the two, and the note's own
	// description of it is underspecified.

	values := url.Values{}
	var rawSuffix string
	for k, v := range inputs {
		rendered, err := render(string(v), tc)
		if err != nil {
			return nil, err
		}
		if k == "$raw" {
			rawSuffix = rendered
			continue
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
		parsed.RawQuery = appendRaw(values.Encode(), rawSuffix)
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("cardigann: build request: %w", RedactErr(err))
		}
	} else {
		body := appendRaw(values.Encode(), rawSuffix)
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

// appendRaw appends a rendered "$raw" input after an already-encoded
// query/form string, matching Cardigann's own semantics: $raw carries its
// own leading separator (0dayfiles' `&categories[]=...`) when there is
// already other content, and loses only a leading "&" when it is the only
// content.
func appendRaw(encoded, raw string) string {
	if raw == "" {
		return encoded
	}
	if encoded == "" {
		return strings.TrimPrefix(raw, "&")
	}
	return encoded + raw
}

// extractRows locates each result row within doc per rows.Selector (and,
// for JSON bodies, one further Attribute descent — e.g. 0dayfiles-api.yml's
// `attribute: attributes`). rows.Count, when set, short-circuits to zero
// rows when it resolves to "0" or empty.
func extractRows(ctx context.Context, doc Doc, rows RowsBlock, tc *TemplateContext) ([]Doc, error) {
	if rows.After != 0 || rows.DateHeaders != nil {
		return nil, ErrUnsupportedRowFeature
	}
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
	if rows.Attribute == "" {
		return base, nil
	}
	var out []Doc
	for _, r := range base {
		sub, ok := r.Select(rows.Attribute)
		if !ok {
			if rows.MissingAttributeEqualsNoResults {
				continue
			}
			return nil, fmt.Errorf("cardigann: row missing attribute %q", rows.Attribute)
		}
		out = append(out, sub)
	}
	return out, nil
}

// evaluateRow walks fields in file order, extracting each into result;
// tc.Result is set to the same map so later fields can read earlier ones
// via .Result.<name> (1337x's title_optional/title_default/title chain).
// A canonical field name (note §3.8) whose Extract misses (ok == false)
// drops the row; a non-canonical/intermediate name is simply left unset.
func evaluateRow(ctx context.Context, fields OrderedFields, tc *TemplateContext, row Doc) (map[string]string, bool, error) {
	result := make(map[string]string, len(fields))
	tc.Result = result
	for _, entry := range fields {
		val, ok, err := entry.Block.Extract(ctx, row, tc)
		if err != nil {
			return nil, false, fmt.Errorf("cardigann: field %q: %w", entry.Name, err)
		}
		if ok {
			result[entry.Name] = val
			continue
		}
		if canonicalFieldNames[entry.Name] {
			logging.FromContext(ctx).Debug("cardigann: dropping row: required field missing", "field", entry.Name)
			return nil, false, nil
		}
	}
	return result, true, nil
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
func mapResultToRelease(def *Definition, cfg Config, result map[string]string, mapper CategoryMapper) torznab.Release {
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
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			rel.PubDate = t
		}
	}

	if v := result["category"]; v != "" {
		rel.Categories = append(rel.Categories, mapper.FromTracker(v)...)
	}
	if v := result["categorydesc"]; v != "" {
		rel.Categories = append(rel.Categories, mapper.FromTrackerDesc(v)...)
	}

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
