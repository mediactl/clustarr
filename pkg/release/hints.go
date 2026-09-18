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

import (
	"strings"

	"github.com/moistari/rls"
)

// streamingServiceVocabulary is the bounded set of streaming-service scene
// tags Hints.Streaming recognizes, drawn from docs/research/quality.md's
// streaming-service custom-format shapes: the "Streaming services" bullet
// (§6.2's custom-format inventory, ~line 292: amzn/amazon(hd)? required AND
// Source WEBDL|WEBRIP) and the Sonarr/Radarr anime streaming set quoted a
// few lines below it (~line 300: CR, DSNP, NF, AMZN, VRV, FUNi, ABEMA, the
// Anime Digital Network abbreviation below, Bilibili/B-Global/HIDIVE), plus
// SourceRegex's own webdl service literals
// (§7.1, ~line 383/425: AmazonHD, iTunesHD, NetflixHD, HBOMaxHD, DisneyHD,
// AMZN, NF, DP, ATVP). Common real-world scene tags for services
// quality.md's excerpts don't separately spell out (HULU, HMAX, MAX, PCOK,
// PMTP, iP, STAN, DCU, RED, iT, WKN) round it out. Checked case-
// insensitively against whatever moistari/rls tags as a Release.Collection
// — rls's own dedicated field for this token (confirmed empirically: it
// reports "ATVP"/"AMZN"/"HULU"/... in Collection, not in Other, which
// carries unrelated revision/edition tags like REMUX/REPACK/PROPER instead)
// — so an edition-style "Collection" tag (e.g. "Criterion") rls might tag
// outside this vocabulary is routed nowhere: the Produces block has no
// Hints.Other field to catch it in, and it isn't a streaming service.
var streamingServiceVocabulary = map[string]bool{
	"AMZN": true, "NF": true, "DSNP": true, "ATVP": true, "HULU": true,
	"HMAX": true, "MAX": true, "PCOK": true, "PMTP": true, "CR": true,
	"IP": true, "STAN": true, "DCU": true, "RED": true, "IT": true,
	"VRV": true, "FUNI": true, "ABEMA": true, "DP": true,
	"BILIBILI": true, "HIDIVE": true, "WKN": true,
	"ADN": true, //nolint:misspell // Anime Digital Network, a real streaming service, not a typo of "AND"
}

// parseHints uses moistari/rls purely as a tokenizer/hint extractor for
// custom-format tags (codec, HDR, audio, streaming service, container) —
// never for quality identity, which parseQualityTags (quality.go) owns via
// its own ported *arr regexes (docs/research/quality.md §7.3).
func parseHints(title string) Hints {
	r := rls.ParseString(title)

	var streaming []string
	if r.Collection != "" && streamingServiceVocabulary[strings.ToUpper(r.Collection)] {
		streaming = []string{r.Collection}
	}

	return Hints{
		Codec:     r.Codec,
		HDR:       r.HDR,
		Audio:     r.Audio,
		Channels:  r.Channels,
		Streaming: streaming,
		Container: r.Container,
	}
}
