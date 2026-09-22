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
	"net/url"
	"strings"

	"github.com/mediactl/clustarr/pkg/cardigann"
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

// redactRawURL is cardigann.RedactURL for a URL still in string form,
// including one url.Parse rejects: everything from the first "?" on is
// dropped, so an unparseable URL cannot leak its query either.
//
// The stripping itself is cardigann's, not this package's. RedactURL and
// RedactErr are exported precisely so indexarr calls them (Ruling R26, named
// in pkg/cardigann/engine.go:147-152; RedactURL is :153 and RedactErr :188): a
// second, independent implementation of
// "take the passkey out" is how the two drift and a secret eventually reaches
// a screen. cardigann's own redactRawURL is unexported, so only this
// string-form wrapper lives here -- and it is a wrapper, not a copy.
func redactRawURL(raw string) string {
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil {
		return cardigann.RedactURL(u)
	}
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i]
	}
	return raw
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
