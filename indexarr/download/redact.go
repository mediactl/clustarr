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

package download

import (
	"errors"
	"net/url"
	"strings"
)

const (
	// maxErrorChars bounds DownloadResponse.Error. It lands in another
	// controller's status, and an unbounded status string is how operators
	// melt etcd.
	maxErrorChars = 512

	// minScrubLen is the shortest secret value worth replacing. A
	// two-character "password" would redact ordinary English out of every
	// message.
	minScrubLen = 8
)

// redactURL renders u for a diagnostic -- a log line, an error, a span
// attribute -- with everything secret stripped: query string, fragment and
// userinfo. Scheme, host and path survive, which is what keeps the message
// diagnosable.
//
// Indexer download links put passkey, apikey and rsskey in the query string.
// These messages reach DownloadResponse.Error, which grabarr writes onto a
// Download's status condition, which a human then reads off a terminal.
// pkg/cardigann makes the same choice for the same reason (engine.go:145);
// its helpers are unexported, so this is a deliberate copy, not an oversight.
// A shared pkg/redact would collapse the three copies and is a carried item.
func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	safe := *u
	safe.RawQuery, safe.ForceQuery = "", false
	safe.Fragment, safe.RawFragment = "", ""
	safe.User = nil
	return safe.String()
}

// redactRawURL is redactURL for a URL still in string form, including one
// url.Parse rejects: everything from the first "?" on is dropped, so an
// unparseable URL cannot leak its query either.
func redactRawURL(raw string) string {
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil {
		return redactURL(u)
	}
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i]
	}
	return raw
}

// redactErr strips the URL net/url puts in *url.Error's own message while
// keeping the underlying cause, so errors.Is still finds context.Canceled,
// syscall errors and the rest through it.
func redactErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

// scrubber returns a function that replaces each secret value in a diagnostic
// with "***". It is the last line of defence: the layers above already avoid
// putting a secret in a message, and this catches the case where a third
// party echoed one back at us.
func scrubber(secrets []string) func(string) string {
	var pairs []string
	for _, s := range secrets {
		if len(s) >= minScrubLen {
			pairs = append(pairs, s, "***")
		}
	}
	if len(pairs) == 0 {
		return func(s string) string { return s }
	}
	return strings.NewReplacer(pairs...).Replace
}

// truncate bounds an attacker-controlled string. The ellipsis is inside the
// budget, so the result is never longer than max.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen < 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
