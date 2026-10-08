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

package iptv

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// redactedURL stands in for a string that does not parse as an absolute URL.
const redactedURL = "<redacted url>"

// Redact returns raw with every part that can carry a credential removed:
// the userinfo, the query, the fragment and every path segment but the last
// (an Xtream Codes stream URL is /live/<user>/<password>/<id>.ts). It keeps
// the scheme, host and last segment, which is enough to tell sources apart
// in a status message or a log.
func Redact(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.Scheme == "" {
		return redactedURL
	}
	var segs []string
	for _, s := range strings.Split(u.Path, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	path := "/"
	switch {
	case len(segs) == 1:
		path = "/" + segs[0]
	case len(segs) > 1:
		path = "/…/" + segs[len(segs)-1]
	}
	return u.Scheme + "://" + u.Host + path
}

// RedactError returns err's text with the URL of every *url.Error in its
// chain redacted. net/http puts the request URL, credentials included, into
// its errors, so a fetch error must pass through here before it reaches a
// status, a log or an Event.
func RedactError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	for e := err; e != nil; e = errors.Unwrap(e) {
		var ue *url.Error
		if !errors.As(e, &ue) {
			break
		}
		// url.Error prints its URL quoted (%q), so a URL with a control
		// character, a Secret's trailing newline among them, appears only
		// escaped: replace that form first, then the raw one.
		msg = strings.ReplaceAll(msg, strconv.Quote(ue.URL), strconv.Quote(Redact(ue.URL)))
		msg = strings.ReplaceAll(msg, ue.URL, Redact(ue.URL))
		e = ue
	}
	return msg
}
