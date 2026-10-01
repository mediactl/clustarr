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

// Package redact renders URLs, and the errors net/http builds around them,
// without the credentials they carry.
//
// Indexers, trackers and list providers authenticate in the URL: Torznab's
// ?apikey=, a tracker's ?passkey= (or a passkey path segment), Plex's
// ?X-Plex-Token=, MDBList's ?apikey=, userinfo. http.Client.Do and url.Parse
// return a *url.Error whose message quotes that whole URL, and those errors
// reach Indexer and Download status, Kubernetes Events, logs and spans. This
// package is the leaf every such client can import -- it imports only the
// standard library, so pkg/torznab (which pkg/cardigann imports) can use it
// without a cycle.
//
// Redaction drops the whole query string rather than a denylist of
// parameter names: a Cardigann definition or a tracker names its secret
// whatever it likes (rsskey, uid, sig, pass), and a denylist misses the one
// nobody thought of. Scheme, host and path survive, which is what makes a
// message diagnosable.
package redact

import (
	"net/url"
	"strings"
)

// unparseable stands in for a URL that cannot be parsed and has no
// recognisable scheme, so nothing of it can be shown safely.
const unparseable = "<unparseable URL>"

// URL renders raw without userinfo, query string or fragment. A URL
// url.Parse rejects is cut at its first '?' or '#' and loses any userinfo
// by hand, so an unparseable URL cannot leak its query either.
func URL(raw string) string {
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil {
		return stripURL(u)
	}
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	return stripUserinfo(raw)
}

// Host renders raw as scheme://host[:port] alone. Use it where the path
// itself may carry a credential: a tracker's .torrent link is often
// /download/<passkey>/<id>.torrent and a private RSS link /rss/<passkey>.
func Host(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return unparseable
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
}

// Err returns err with the URL of a *url.Error (what http.Client.Do and
// url.Parse return) rendered by [URL]. Op and the cause are kept, and the
// result is still a *url.Error, so errors.Is (context.DeadlineExceeded,
// context.Canceled, a syscall error), errors.As and net.Error's Timeout all
// behave exactly as on the original.
//
// Apply it to the error Do or Parse returned, before wrapping it: a
// *url.Error already wrapped inside another error has put its URL into
// that error's message, which cannot be taken back out. Any other error is
// returned unchanged.
func Err(err error) error { return rewrite(err, URL) }

// ErrHost is [Err] with the URL rendered by [Host], for a URL whose path
// may carry a credential.
func ErrHost(err error) error { return rewrite(err, Host) }

func rewrite(err error, render func(string) string) error {
	// Only a top-level *url.Error can be rewritten; see Err.
	ue, ok := err.(*url.Error) //nolint:errorlint // deliberately not errors.As
	if !ok || ue == nil {
		return err
	}
	return &url.Error{Op: ue.Op, URL: render(ue.URL), Err: ue.Err}
}

func stripURL(u *url.URL) string {
	if u.Opaque != "" {
		// "scheme:opaque" has no authority or path to keep apart from
		// whatever it carries.
		return u.Scheme + ":"
	}
	safe := *u
	safe.User = nil
	safe.RawQuery = ""
	safe.ForceQuery = false
	safe.Fragment = ""
	safe.RawFragment = ""
	return safe.String()
}

// stripUserinfo removes "user:pass@" from the authority of a URL url.Parse
// rejected.
func stripUserinfo(raw string) string {
	i := strings.Index(raw, "://")
	if i < 0 {
		// No authority to find; an '@' anywhere means userinfo may be in
		// there in a shape this cannot delimit.
		if strings.ContainsRune(raw, '@') {
			return unparseable
		}
		return raw
	}
	rest := raw[i+3:]
	end := strings.IndexByte(rest, '/')
	if end < 0 {
		end = len(rest)
	}
	if at := strings.LastIndexByte(rest[:end], '@'); at >= 0 {
		rest = rest[at+1:]
	}
	return raw[:i+3] + rest
}
