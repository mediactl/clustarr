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

package release

import "github.com/moistari/rls"

// parseHints uses moistari/rls purely as a tokenizer/hint extractor for
// custom-format tags (codec, HDR, audio, streaming service, container) —
// never for quality identity, which parseQualityTags (quality.go) owns via
// its own ported *arr regexes (docs/research/quality.md §7.3).
func parseHints(title string) Hints {
	r := rls.ParseString(title)
	return Hints{
		Codec:     r.Codec,
		HDR:       r.HDR,
		Audio:     r.Audio,
		Channels:  r.Channels,
		Streaming: r.Other, // rls has no dedicated streaming-service field; Other carries it
		Container: r.Container,
	}
}
