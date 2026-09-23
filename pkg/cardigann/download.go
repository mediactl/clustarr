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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Download resolves link (a search result's Details/Download field, or
// already a "magnet:" URI, in which case it is returned unread) into the
// downloadable content: with no DownloadBlock, link is fetched directly;
// otherwise Before (if set, its path taken from PathSelector on link's page
// when that is set) runs first, link's page is fetched and each Selectors
// entry is tried in file order (selector strings are themselves templates —
// see 1337x's `a[href*="{{ .Config.primarydownloadlink }}"]`); the first
// non-empty match is resolved against link and fetched (or returned as-is
// when it is itself a "magnet:" URI); with no Selectors match, InfoHash (if
// set) builds a magnet URI from the extracted hash and title.
//
// testlinktorrent (default true, Prowlarr's CardigannDefinition default)
// makes a selector's fetched file prove it is a torrent -- a bencoded
// dictionary starts with 'd' -- before it is returned; a match that serves
// an HTML "download limit reached" page moves on to the next selector
// instead of handing the download client a web page. Prowlarr accepts an
// empty body here, and so does this.
//
// Every request carries download.headers, else search.headers (Prowlarr's
// `Download?.Headers ?? Search?.Headers`): an API tracker that authenticates
// search with an Authorization header needs it on the download too.
func (e Engine) Download(ctx context.Context, def *Definition, cfg Config, link string) (io.ReadCloser, error) {
	// A magnet link needs no network access at all — it is returned
	// as-is regardless of whether this definition requires a login
	// session, so that check is deliberately below this one.
	if strings.HasPrefix(link, "magnet:") {
		return io.NopCloser(strings.NewReader(link)), nil
	}
	if loginRequiresSession(def.Login) && cfg.Session == nil {
		return nil, ErrSessionRequired
	}
	tc := e.templateContext(def, cfg)
	headers := downloadHeaders(def)
	if def.Download == nil {
		body, err := e.fetch(ctx, def, cfg, tc, headers, link)
		if err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(body)), nil
	}

	db := def.Download
	pageURL, err := resolveURL(cfg.BaseURL, link)
	if err != nil {
		return nil, err
	}

	var page *Doc
	loadPage := func() (Doc, error) {
		if page != nil {
			return *page, nil
		}
		body, err := e.fetch(ctx, def, cfg, tc, headers, pageURL)
		if err != nil {
			return Doc{}, err
		}
		if body, err = decodeBody(tc.enc, body); err != nil {
			return Doc{}, err
		}
		d, err := ParseDoc(ResponseHTML, body)
		if err != nil {
			return Doc{}, err
		}
		page = &d
		return d, nil
	}

	var beforeDoc Doc
	if db.Before != nil {
		before := *db.Before
		if before.PathSelector != nil {
			d, err := loadPage()
			if err != nil {
				return nil, err
			}
			path, ok, err := extractSelectorField(ctx, d, *before.PathSelector, tc)
			if err != nil {
				return nil, err
			}
			if !ok || path == "" {
				return nil, fmt.Errorf("cardigann: download before.pathselector %q did not match", before.PathSelector.Selector)
			}
			before.Path = path
		}
		d, err := e.runBefore(ctx, def, cfg, tc, headers, &before)
		if err != nil {
			return nil, err
		}
		beforeDoc = d
	}

	doc, err := loadPage()
	if err != nil {
		return nil, err
	}

	for _, sel := range db.Selectors {
		src := doc
		if sel.UseBeforeResponse && db.Before != nil {
			src = beforeDoc
		}
		val, ok, err := extractSelectorField(ctx, src, sel, tc)
		if err != nil {
			return nil, err
		}
		if !ok || val == "" {
			continue
		}
		if strings.HasPrefix(val, "magnet:") {
			return io.NopCloser(strings.NewReader(val)), nil
		}
		target, err := resolveURL(pageURL, val)
		if err != nil {
			return nil, err
		}
		body, err := e.fetch(ctx, def, cfg, tc, headers, target)
		if err != nil {
			return nil, err
		}
		if testsLinks(def) && len(body) > 0 && body[0] != 'd' {
			logging.FromContext(ctx).Debug("cardigann: download selector's link is not a torrent, trying the next",
				"selector", sel.Selector)
			continue
		}
		return io.NopCloser(bytes.NewReader(body)), nil
	}

	if db.InfoHash != nil {
		return e.buildMagnet(ctx, tc, db.InfoHash, beforeDoc, doc)
	}
	return nil, fmt.Errorf("cardigann: no download selector matched for %q", redactRawURL(link))
}

// testsLinks is testlinktorrent with Prowlarr's default of true.
func testsLinks(def *Definition) bool {
	return def.TestLinkTorrent == nil || *def.TestLinkTorrent
}

// downloadHeaders is download.headers, else search.headers.
func downloadHeaders(def *Definition) map[string][]string {
	if def.Download != nil && def.Download.Headers != nil {
		return def.Download.Headers
	}
	return def.Search.Headers
}

// fetch GETs link (resolved against cfg.BaseURL), following redirects, and
// returns the whole body -- bounded by e.do's own maxResponseBodyBytes cap,
// the same 8 MiB limit pkg/torznab.Client uses. The body is returned as
// sent: a torrent file is bytes, not text in the tracker's charset.
func (e Engine) fetch(ctx context.Context, def *Definition, cfg Config, tc *TemplateContext, headers map[string][]string, link string) ([]byte, error) {
	u, err := resolveURL(cfg.BaseURL, link)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("cardigann: build request: %w", RedactErr(err))
	}
	if err := renderHeaders(req, headers, tc); err != nil {
		return nil, err
	}
	attachSession(req, cfg.Session)
	_, body, err := e.do(ctx, req, exchange{def: def, site: cfg.BaseURL, follow: true})
	return body, err
}

// runBefore issues download.before's request and parses its response as
// HTML, for a later Selectors/InfoHash entry with UseBeforeResponse to
// read from. Neither bundled definition (1337x, 0dayfiles-api) sets
// download.before, so this path has no corpus coverage; it is implemented
// to the same request-building rules as search.go's buildSearchRequest.
func (e Engine) runBefore(ctx context.Context, def *Definition, cfg Config, tc *TemplateContext, headers map[string][]string, before *BeforeBlock) (Doc, error) {
	renderedPath, err := render(before.Path, tc)
	if err != nil {
		return Doc{}, err
	}
	method := strings.ToUpper(before.Method)
	if method == "" {
		method = http.MethodGet
	}
	values := url.Values{}
	for k, v := range before.Inputs {
		rendered, err := render(string(v), tc)
		if err != nil {
			return Doc{}, err
		}
		values.Set(k, rendered)
	}
	u, err := resolveURL(cfg.BaseURL, renderedPath)
	if err != nil {
		return Doc{}, err
	}

	var req *http.Request
	if method == http.MethodGet {
		parsed, err := url.Parse(u)
		if err != nil {
			return Doc{}, fmt.Errorf("cardigann: before url %q: %w", redactRawURL(u), RedactErr(err))
		}
		if q := encodeValues(values, tc.enc, before.QuerySeparator); q != "" {
			parsed.RawQuery = q
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			return Doc{}, fmt.Errorf("cardigann: build request: %w", RedactErr(err))
		}
	} else {
		req, err = http.NewRequestWithContext(ctx, method, u, strings.NewReader(encodeValues(values, tc.enc, "")))
		if err != nil {
			return Doc{}, fmt.Errorf("cardigann: build request: %w", RedactErr(err))
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if err := renderHeaders(req, headers, tc); err != nil {
		return Doc{}, err
	}
	attachSession(req, cfg.Session)
	_, body, err := e.do(ctx, req, exchange{def: def, site: cfg.BaseURL, follow: true})
	if err != nil {
		return Doc{}, err
	}
	if body, err = decodeBody(tc.enc, body); err != nil {
		return Doc{}, err
	}
	return ParseDoc(ResponseHTML, body)
}

// extractSelectorField is SelectorField's Extract-style evaluation: render
// the selector as a template, narrow d, read Attribute, run Filters in
// order. SelectorField is the schema's smaller selector shape (no
// case/default/text), so it does not go through SelectorBlock.Extract.
func extractSelectorField(ctx context.Context, d Doc, sf SelectorField, tc *TemplateContext) (string, bool, error) {
	renderedSelector, err := render(sf.Selector, tc)
	if err != nil {
		return "", false, err
	}
	found, ok := d.Select(renderedSelector)
	if !ok {
		return "", false, nil
	}
	val, ok := found.Text(sf.Attribute)
	if !ok {
		return "", false, nil
	}
	for _, f := range sf.Filters {
		fn, known := Filters[f.Name]
		if !known {
			return "", false, fmt.Errorf("cardigann: unknown filter %q", f.Name)
		}
		val, err = fn(ctx, val, []string(f.Args), tc)
		if err != nil {
			return "", false, fmt.Errorf("cardigann: filter %q: %w", f.Name, err)
		}
	}
	return val, true, nil
}

// buildMagnet extracts InfoHash's Hash/Title and formats
// "magnet:?xt=urn:btih:<hash>&dn=<title>" — no announce trackers, since
// InfoHashBlock carries none (a difference from the research note's
// parenthetical "hash+title+trackers"; the schema's actual InfoHashBlock
// only has Hash/Title/UseBeforeResponse — the schema wins over the note).
func (e Engine) buildMagnet(ctx context.Context, tc *TemplateContext, ih *InfoHashBlock, beforeDoc, doc Doc) (io.ReadCloser, error) {
	hashSrc := doc
	if ih.Hash.UseBeforeResponse {
		hashSrc = beforeDoc
	}
	hash, ok, err := extractSelectorField(ctx, hashSrc, ih.Hash, tc)
	if err != nil {
		return nil, err
	}
	if !ok || hash == "" {
		return nil, fmt.Errorf("cardigann: infohash selector %q did not match", ih.Hash.Selector)
	}

	titleSrc := doc
	if ih.Title.UseBeforeResponse {
		titleSrc = beforeDoc
	}
	title, ok, err := extractSelectorField(ctx, titleSrc, ih.Title, tc)
	if err != nil {
		return nil, err
	}

	magnet := "magnet:?xt=urn:btih:" + hash
	if ok && title != "" {
		magnet += "&dn=" + url.QueryEscape(title)
	}
	return io.NopCloser(strings.NewReader(magnet)), nil
}
