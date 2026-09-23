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
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/htmlindex"
)

// Definition.Encoding is the tracker's character set, and Prowlarr uses it
// three ways (CardigannBase's _encoding, set from Encoding.GetEncoding):
// every request's query string and form body is percent-encoded in it
// (HttpRequestBuilder.Encoding, GetQueryString(_encoding)); every response
// is decoded with it (HttpResponse.Content prefers Request.Encoding over the
// Content-Type charset); and the urlencode/urldecode filters use it. The
// corpus is 92% UTF-8; the rest are the Cyrillic, Central European, Thai,
// Arabic and Hebrew single-byte sets (windows-1251 alone is 18 trackers),
// where a search for a non-ASCII title sent as UTF-8 finds nothing and a
// page decoded as UTF-8 yields mojibake titles.
//
// Names resolve through the WHATWG encoding registry (htmlindex), which is
// what a browser does with the same label and covers every name the v11
// corpus uses. The one difference from .NET is deliberate and harmless:
// WHATWG reads "iso-8859-1" as its superset windows-1252.

// textEncoding resolves d.Encoding: nil for UTF-8 (or unset -- nothing to
// transcode), the encoding otherwise, and an ErrInvalidDefinition error for
// a name the registry does not know.
func (d *Definition) textEncoding() (encoding.Encoding, error) {
	name := strings.TrimSpace(d.Encoding)
	if name == "" {
		return nil, nil
	}
	enc, err := htmlindex.Get(name)
	if err != nil {
		return nil, fmt.Errorf("%w: unsupported encoding %q", ErrInvalidDefinition, d.Encoding)
	}
	if canonical, _ := htmlindex.Name(enc); canonical == "utf-8" {
		return nil, nil
	}
	return enc, nil
}

// decodeBody converts a response body from enc to UTF-8 before it is
// parsed. A nil enc (UTF-8) returns body unchanged.
func decodeBody(enc encoding.Encoding, body []byte) ([]byte, error) {
	if enc == nil {
		return body, nil
	}
	out, err := enc.NewDecoder().Bytes(body)
	if err != nil {
		return nil, fmt.Errorf("cardigann: decode response: %w", err)
	}
	return out, nil
}

// toCharset converts s to enc's bytes, replacing what enc cannot represent
// the way .NET's Encoding.GetBytes does (with "?"), so an unmappable
// character degrades one search term rather than failing the request.
func toCharset(s string, enc encoding.Encoding) string {
	if enc == nil {
		return s
	}
	out, err := encoding.ReplaceUnsupported(enc.NewEncoder()).String(s)
	if err != nil {
		return s
	}
	return out
}

// fromCharset converts enc's bytes in s to UTF-8.
func fromCharset(s string, enc encoding.Encoding) (string, error) {
	if enc == nil {
		return s, nil
	}
	return enc.NewDecoder().String(s)
}

// queryEscape is url.QueryEscape in enc: the value is converted to the
// tracker's charset first, then each byte outside the unreserved set is
// percent-encoded -- "é" is %E9 in windows-1252, not UTF-8's %C3%A9.
func queryEscape(s string, enc encoding.Encoding) string {
	return url.QueryEscape(toCharset(s, enc))
}

// encodeValues is url.Values.Encode in enc, joined by sep (default "&",
// RequestBlock.Queryseparator's default in Prowlarr). Keys are sorted, as
// Encode sorts them, so a request is deterministic.
func encodeValues(v url.Values, enc encoding.Encoding, sep string) string {
	if sep == "" {
		sep = "&"
	}
	if enc == nil && sep == "&" {
		return v.Encode()
	}
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		ek := queryEscape(k, enc)
		for _, val := range v[k] {
			if b.Len() > 0 {
				b.WriteString(sep)
			}
			b.WriteString(ek)
			b.WriteByte('=')
			b.WriteString(queryEscape(val, enc))
		}
	}
	return b.String()
}

// sha1Hex matches one Definition.Certificates entry: a SHA-1 fingerprint,
// forty hex digits (the corpus writes them lower-case; Jackett upper-cases
// before comparing, so either case is accepted).
var sha1Hex = regexp.MustCompile(`^[0-9A-Fa-f]{40}$`)

// checkEngineConstraints refuses a schema-valid definition whose values this
// engine cannot honour, so the failure is reported once, at Load, rather
// than on every search: an encoding the registry does not know, or a
// certificate that is not a SHA-1 fingerprint (the schema types it only as a
// string).
func (d *Definition) checkEngineConstraints() error {
	if _, err := d.textEncoding(); err != nil {
		return err
	}
	for _, c := range d.Certificates {
		if !sha1Hex.MatchString(c) {
			return fmt.Errorf("%w: certificate %q is not a SHA-1 fingerprint", ErrInvalidDefinition, c)
		}
	}
	return nil
}

// SiteLink returns the base URL requests for d should use when the operator
// configured configured: d.Links[0] when configured is empty or is one of
// d.LegacyLinks, configured otherwise. This is Prowlarr's
// CardigannBase.ResolveSiteLink -- a tracker that moved domain lists its old
// addresses as legacylinks, and an indexer still pointing at one is moved to
// the current address rather than left talking to a dead or hijacked domain.
// The comparison ignores a trailing slash and the scheme/host case.
func (d *Definition) SiteLink(configured string) string {
	if strings.TrimSpace(configured) == "" {
		if len(d.Links) > 0 {
			return d.Links[0]
		}
		return configured
	}
	if len(d.Links) == 0 {
		return configured
	}
	want := normalizeLink(configured)
	for _, legacy := range d.LegacyLinks {
		if normalizeLink(legacy) == want {
			return d.Links[0]
		}
	}
	return configured
}

// normalizeLink is the comparison key SiteLink matches on.
func normalizeLink(link string) string {
	link = strings.TrimSpace(link)
	if u, err := url.Parse(link); err == nil && u.Host != "" {
		u.Scheme = strings.ToLower(u.Scheme)
		u.Host = strings.ToLower(u.Host)
		link = u.String()
	}
	return strings.TrimSuffix(link, "/")
}
