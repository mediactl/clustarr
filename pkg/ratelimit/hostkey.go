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

package ratelimit

import "net/url"

// HostKey is the [Limiter] key for an endpoint that is paced per HOST:
// rawURL's host[:port], and nothing else.
//
// It lives here, in the package that owns the limiter, because the key is
// what makes two callers address ONE bucket. Two Indexers pointing at one
// tracker -- a Prowlarr or Jackett instance in front of many trackers, or a
// torrent and a usenet Indexer on one server -- must draw on one budget, and
// so must two different VERBS against the same host: indexarr's reconciler
// calls [Limiter.SetConfig] with this key and its search, RSS and download
// paths only ever Wait on it.
//
// A divergence between two spellings of the key is not "paced twice as fast".
// [Limiter.Wait] falls back to the Limiter's own defaults for a key with no
// Config, and Config.RPS <= 0 means rate.Inf, so a caller that spells the key
// differently from the one that called SetConfig is COMPLETELY UNPACED
// whenever the Limiter was built as New(Config{}) -- which is the obvious way
// to build it when every real config arrives through SetConfig. Private
// trackers ban for exactly that, which is why one exported function is worth
// more here than the three lines it saves.
//
// It returns "" rather than an error for a malformed URL: every caller
// already has, or is about to produce, a better error about the URL itself,
// and a key helper that can fail is a key helper callers skip. A "" key is
// still a perfectly good bucket -- it simply pools every unparseable
// endpoint, which is the safe direction to fail.
func HostKey(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Host
}
