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
)

// Download resolves link (a search result's Details/Download field, or
// already a "magnet:" URI, in which case it is returned unread) into the
// downloadable content: with no DownloadBlock, link is fetched directly;
// otherwise Before (if set) runs first, link's page is fetched and each
// Selectors entry is tried in file order (selector strings are themselves
// templates — see 1337x's `a[href*="{{ .Config.primarydownloadlink }}"]`);
// the first non-empty match is fetched (or returned as-is when it is itself
// a "magnet:" URI); with no Selectors match, InfoHash (if set) builds a
// magnet URI from the extracted hash and title.
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
	if def.Download == nil {
		return e.fetch(ctx, cfg, link)
	}

	db := def.Download
	tc := e.templateContext(def, cfg)

	var beforeDoc Doc
	if db.Before != nil {
		d, err := e.runBefore(ctx, cfg, tc, db.Before)
		if err != nil {
			return nil, err
		}
		beforeDoc = d
	}

	page, err := e.fetch(ctx, cfg, link)
	if err != nil {
		return nil, err
	}
	defer func() { _ = page.Close() }()
	pageBytes, err := io.ReadAll(page)
	if err != nil {
		return nil, fmt.Errorf("cardigann: read download page: %w", err)
	}
	doc, err := ParseDoc(ResponseHTML, pageBytes)
	if err != nil {
		return nil, err
	}

	for _, sel := range db.Selectors {
		src := doc
		if sel.UseBeforeResponse {
			src = beforeDoc
		}
		val, ok, err := extractSelectorField(ctx, src, sel, tc)
		if err != nil {
			return nil, err
		}
		if !ok || val == "" {
			continue
		}
		return e.resolveLink(ctx, cfg, val)
	}

	if db.InfoHash != nil {
		return e.buildMagnet(ctx, tc, db.InfoHash, beforeDoc, doc)
	}
	return nil, fmt.Errorf("cardigann: no download selector matched for %q", redactRawURL(link))
}

// resolveLink fetches val, or — when it is itself a "magnet:" URI —
// returns it unread, exactly like Download's own top-level check.
func (e Engine) resolveLink(ctx context.Context, cfg Config, val string) (io.ReadCloser, error) {
	if strings.HasPrefix(val, "magnet:") {
		return io.NopCloser(strings.NewReader(val)), nil
	}
	return e.fetch(ctx, cfg, val)
}

// fetch GETs link (resolved against cfg.BaseURL) and buffers the whole
// body — bounded by e.do's own maxResponseBodyBytes cap, the same 8 MiB
// limit pkg/torznab.Client uses.
func (e Engine) fetch(ctx context.Context, cfg Config, link string) (io.ReadCloser, error) {
	u, err := resolveURL(cfg.BaseURL, link)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("cardigann: build request: %w", redactErr(err))
	}
	attachSession(req, cfg.Session)
	_, body, err := e.do(ctx, req)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

// runBefore issues download.before's request and parses its response as
// HTML, for a later Selectors/InfoHash entry with UseBeforeResponse to
// read from. Neither bundled definition (1337x, 0dayfiles-api) sets
// download.before, so this path has no corpus coverage; it is implemented
// to the same request-building rules as search.go's buildSearchRequest.
func (e Engine) runBefore(ctx context.Context, cfg Config, tc *TemplateContext, before *BeforeBlock) (Doc, error) {
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
			return Doc{}, fmt.Errorf("cardigann: before url %q: %w", redactRawURL(u), redactErr(err))
		}
		if q := values.Encode(); q != "" {
			parsed.RawQuery = q
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			return Doc{}, fmt.Errorf("cardigann: build request: %w", redactErr(err))
		}
	} else {
		req, err = http.NewRequestWithContext(ctx, method, u, strings.NewReader(values.Encode()))
		if err != nil {
			return Doc{}, fmt.Errorf("cardigann: build request: %w", redactErr(err))
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	attachSession(req, cfg.Session)
	_, body, err := e.do(ctx, req)
	if err != nil {
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
