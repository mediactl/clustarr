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
// when that is set) runs first. Then, in Prowlarr's order
// (CardigannRequestGenerator.DownloadRequest, develop): an InfoHash block,
// when the definition has one, builds a magnet URI from the extracted hash
// and title, and Selectors are not consulted at all -- Prowlarr's
// `if (download.Infohash != null) ... else if (download.Selectors ...)`.
// Otherwise each Selectors entry is tried in file order (selector strings
// are themselves templates -- see 1337x's
// `a[href*="{{ .Config.primarydownloadlink }}"]`); the first non-empty match
// is resolved against link and fetched (or returned as-is when it is itself
// a "magnet:" URI). Until gap fix Z6 the selectors were tried first and the
// infohash only after none matched. Where Prowlarr, on an infohash that does
// not match, falls through to requesting link itself -- the details page,
// handed to the download client as if it were a torrent -- this returns the
// error. An infohash or selector with usebeforeresponse reads the before
// response instead of link's page (the infohash block's own flag, as
// Prowlarr reads it, or its hash/title field's).
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
	pageURL, err := resolveURL(cfg.BaseURL, link)
	if err != nil {
		return nil, err
	}
	if u, err := url.Parse(pageURL); err == nil {
		tc.DownloadUri = uriVars(u)
	}
	headers := downloadHeaders(def)
	if def.Download == nil {
		body, err := e.fetch(ctx, def, cfg, tc, headers, pageURL)
		if err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(body)), nil
	}

	db := def.Download

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
		d, err := e.runBefore(ctx, def, cfg, tc, headers, &before, pageURL)
		if err != nil {
			return nil, err
		}
		beforeDoc = d
	}

	// source is the document a selector with usebeforeresponse reads: the
	// before response when there was a before request, else link's page.
	source := func(useBefore bool) (Doc, error) {
		if useBefore && db.Before != nil {
			return beforeDoc, nil
		}
		return loadPage()
	}

	if db.InfoHash != nil {
		return e.buildMagnet(ctx, tc, db.InfoHash, source)
	}

	for _, sel := range db.Selectors {
		src, err := source(sel.UseBeforeResponse)
		if err != nil {
			return nil, err
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
// HTML, for a later Selectors/InfoHash entry with UseBeforeResponse to read
// from. It is Prowlarr's HandleRequest: the path resolved against the site,
// inputs as the query of a GET (added to any query the path already has) or
// the form of a POST, download.headers else search.headers, Referer set to
// the link, and redirects NOT followed -- HandleRequest's HttpRequestBuilder
// leaves AllowAutoRedirect at its false default, so a before request that
// answers 302 is read as the 302 it is. Until gap fix Z6 it followed them,
// and a before path's own query was replaced by its inputs. 23 of the 752
// bundled definitions set download.before.
func (e Engine) runBefore(ctx context.Context, def *Definition, cfg Config, tc *TemplateContext, headers map[string][]string, before *BeforeBlock, referer string) (Doc, error) {
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
			if parsed.RawQuery != "" {
				q = parsed.RawQuery + "&" + q
			}
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
	req.Header.Set("Referer", referer)
	attachSession(req, cfg.Session)
	_, body, err := e.do(ctx, req, exchange{def: def, site: cfg.BaseURL})
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
// "magnet:?xt=urn:btih:<hash>&dn=<title>" -- no announce trackers, since
// InfoHashBlock carries none (a difference from the research note's
// parenthetical "hash+title+trackers"; the schema's actual InfoHashBlock
// only has Hash/Title/UseBeforeResponse -- the schema wins over the note).
// source yields the page each half reads: the before response when the
// block's usebeforeresponse is set -- the level Prowlarr reads, and the one
// the bundled kinozal-magnet and magnetdownload definitions set -- or the
// field's own, else link's page.
func (e Engine) buildMagnet(ctx context.Context, tc *TemplateContext, ih *InfoHashBlock, source func(useBefore bool) (Doc, error)) (io.ReadCloser, error) {
	hashSrc, err := source(ih.UseBeforeResponse || ih.Hash.UseBeforeResponse)
	if err != nil {
		return nil, err
	}
	hash, ok, err := extractSelectorField(ctx, hashSrc, ih.Hash, tc)
	if err != nil {
		return nil, err
	}
	if !ok || hash == "" {
		return nil, fmt.Errorf("cardigann: infohash selector %q did not match", ih.Hash.Selector)
	}

	titleSrc, err := source(ih.UseBeforeResponse || ih.Title.UseBeforeResponse)
	if err != nil {
		return nil, err
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
