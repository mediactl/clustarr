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

package subtitles

import "context"

// Capabilities describes what a Provider can search and serve.
type Capabilities struct {
	Movies, Episodes bool
	ForcedSearch     bool // false for Bazarr's PROVIDERS_FORCED_OFF equivalents
	HashSearch       bool
	HashVerifiable   bool
	Languages        func(bcp47 string) bool
	NeedsSecrets     []string // "apiKey", "username", "password"
	Generates        bool     // true for a provider that fabricates rather than fetches (not shipped by B8)
}

// Provider is spec §7's Name/Search/Download/HIVerifiable plus Capabilities
// (this task's explicit ask). Download returns the served file name
// alongside the bytes — spec's literal 3-return signature
// ([]byte, string, error) — because OpenSubtitles' /download response
// carries file_name for format/extension sniffing before PostProcess runs.
type Provider interface {
	Name() string
	Capabilities() Capabilities
	Search(ctx context.Context, q Query) ([]Candidate, error)
	Download(ctx context.Context, c Candidate) (raw []byte, fileName string, err error)
	HIVerifiable() bool
}
