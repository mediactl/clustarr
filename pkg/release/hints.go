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
// streaming-service custom-format shapes (the "Streaming services" bullet,
// §6.2's custom-format inventory ~line 292: amzn/amazon(hd)? required AND
// Source WEBDL|WEBRIP; the Sonarr/Radarr anime streaming set quoted a few
// lines below it, ~line 300: CR, DSNP, NF, AMZN, VRV, FUNi; and
// SourceRegex's own webdl service literals, §7.1 ~line 383/425: AmazonHD,
// iTunesHD, NetflixHD, HBOMaxHD, DisneyHD, AMZN, NF, DP, ATVP) plus common
// real-world scene tags for services quality.md's excerpts don't separately
// spell out (HULU, HMAX, PCOK, PMTP, iPlayer, STAN, DCU, RED, iTunes).
//
// Every key here is cross-checked against rls v0.6.0's own
// taginfo/taginfo.csv (the type=collection rows — moistari/rls's source of
// truth for what it will ever actually set Release.Collection to) rather
// than assumed: a handful of tokens from the sources above that quality.md
// or common scene convention names (ABEMA, the Anime Digital Network
// abbreviation, Bilibili/B-Global, DP, HIDIVE, WKN, a bare "MAX") turned
// out to have no matching row at all (rls has only "HMAX", never bare
// "MAX") and were dropped as dead code rather
// than kept as keys that can never match. "iP"/"iPlayer" and "iT"/"iTunes"
// both canonicalize, per taginfo.csv, to Release.Collection == "iPlayer"/
// "iTunes" (confirmed empirically), hence the "IPLAYER"/"ITUNES" keys below
// rather than "IP"/"IT" — checked case-insensitively against
// strings.ToUpper(r.Collection), so the map keys are themselves upper-case.
//
// An edition-style "Collection" tag rls might report outside this
// vocabulary (e.g. "Criterion.Collection") is routed nowhere: the Produces
// block has no Hints.Other field to catch it in, and it isn't a streaming
// service.
var streamingServiceVocabulary = map[string]bool{
	"AMZN": true, "NF": true, "DSNP": true, "ATVP": true, "HULU": true,
	"HMAX": true, "PCOK": true, "PMTP": true, "CR": true,
	"IPLAYER": true, "STAN": true, "DCU": true, "RED": true, "ITUNES": true,
	"VRV": true, "FUNI": true,
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
