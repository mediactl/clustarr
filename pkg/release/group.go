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

	"github.com/dlclark/regexp2"
)

// The release-group regexes below port Radarr's ReleaseGroupParser
// (src/NzbDrone.Core/Parser/ReleaseGroupParser.cs and ParserCommon.cs,
// develop branch, fetched 2026-09-23), with Sonarr's additions from its own
// Parser.cs folded in where Radarr's lists are narrower: Sonarr's
// ExceptionReleaseGroupRegexExact names (Fight-BB, KCRT, and the looser
// BEN[_. ]THE[_. ]MEN spelling) and CleanReleaseGroupRegex's leading
// "...SxxEyy." strip, which keeps a hyphenated series or episode title from
// being read as a group. Both are GPL-3.0, as is this package.

// animeGroupRegex ports AnimeReleaseGroupRegex verbatim: a bracketed
// sub-group token anchored at the very start of the title.
var animeGroupRegex = mustCompile(`^(?:\[(?<subgroup>(?!\s).+?(?<!\s))\](?:_|-|\s|\.)?)`, regexp2.IgnoreCase)

// websitePrefixRegex ports ParserCommon.WebsitePrefixRegex: a leading
// "[www.site.tld]" / "site.tld - " tracker advert, which would otherwise be
// read as an anime sub-group.
var websitePrefixRegex = mustCompile(
	`^(?:(?:\[|\()\s*)?(?:www\.)?[-a-z0-9-]{1,256}\.(?<!Naruto-Kun\.)(?:[a-z]{2,6}\.[a-z]{2,6}|xn--[a-z0-9-]{4,}|[a-z]{2,})\b(?:\s*(?:\]|\))|[ -]{2,})[ -]*`,
	regexp2.IgnoreCase,
)

// cleanTorrentSuffixRegex ports ParserCommon.CleanTorrentSuffixRegex: a
// trailing public-tracker tag that is not the group.
var cleanTorrentSuffixRegex = mustCompile(`\[(?:ettv|rartv|rarbg|cttv|publichd)\]$`, regexp2.IgnoreCase)

// cleanReleaseGroupRegex ports CleanReleaseGroupRegex: Sonarr's leading
// "title.SxxEyy." strip, and the reposter/indexer suffixes both apps remove
// from the end ("-Obfuscated", "-postbot", "-Rakuv02", ...).
var cleanReleaseGroupRegex = mustCompile(
	`^(?:.*?[-._ ]S\d+E\d+[-._ ])|(?:-(?:RP|1|NZBGeek|Obfuscated|Obfuscation|Scrambled|sample|Pre|postbot|xpost|Rakuv[a-z0-9]*|WhiteRev|BUYMORE|AsRequested|AlternativeToRequested|GEROV|Z0iDS3N|Chamele0n|4P|4Planet|AlteZachen|RePACKPOST))+$`,
	regexp2.IgnoreCase,
)

// exceptionGroupExactRegex ports ExceptionReleaseGroupRegexExact: groups
// that do not follow the trailing "-GROUP" convention (some punctuate with
// a dot, like "x264.YIFY"), so they are recognised by name. The last match
// wins.
var exceptionGroupExactRegex = mustCompile(
	`\b(?<group>KRaLiMaRKo|E\.N\.D|D\-Z0N3|Koten_Gars|BluDragon|ZØNEHD|HQMUX|VARYG|YIFY|YTS(?:.(?:MX|LT|AG))?|TMd|Eml HDTeam|LMain|DarQ|BEN[_. ]THE[_. ]MEN|TAoE|QxR|126811|Fight-BB|KCRT)\b`,
	regexp2.IgnoreCase,
)

// exceptionGroupWrappedRegex ports ExceptionReleaseGroupRegex: groups whose
// releases end "(Group)" or "[Group]". The last match wins.
var exceptionGroupWrappedRegex = mustCompile(
	`(?<=[._ \[])(?<group>(?:Silence|afm72|Panda|Ghost|MONOLITH|Tigole|Joy|ImE|UTR|t3nzin|Anime Time|Project Angel|Hakata Ramen|HONE|GiLG|Vyndros|SEV|Garshasp|Kappa|Natty|RCVR|SAMPA|YOGI|r00t|EDGE2020|RZeroX|FreetheFish|Anna|Bandi|Qman|theincognito|HDO|DusIctv|DHD|CtrlHD|-ZR-|ADC|XZVN|RH|Kametsu|Celdra)(?=\]|\)))`,
	regexp2.IgnoreCase,
)

// releaseGroupRegex ports ReleaseGroupRegex verbatim. The candidate is a
// "-GROUP" (or "-PART-TWO") token that is not followed by a resolution and
// is not the tail of a quality, audio or language token -- the negative
// lookbehind is what keeps "Bluray-1080p", "WEB-DL", "DTS-HD" and "-ENG"
// from being read as groups, which is exactly how both common *arr file
// layouts ("Heat (1995) - Bluray-1080p", "Heat.1995.Bluray-1080p-RlsGrp")
// used to yield "1080p" and "1080p-RlsGrp". The second alternative is a
// trailing " [GROUP]". The LAST match in the title wins.
var releaseGroupRegex = mustCompile(
	`-(?<group>[a-z0-9]+(?<part2>-[a-z0-9]+)?(?!.+?(?:480p|576p|720p|1080p|2160p)))`+
		`(?<!(?:WEB-(?:DL|Rip)|Blu-Ray|480p|576p|720p|1080p|2160p|DTS-HD|DTS-X|DTS-MA|DTS-ES|-ES|-EN|-CAT|-ENG|-JAP|-GER|-FRA|-FRE|-ITA|-HDRip|\d{1,2}-bit|[ ._]\d{4}-\d{2}|-\d{2}|tmdb(?:id)?-\d+|tt\d{7,8})(?:\k<part2>)?)`+
		`(?:\b|[-._ ]|$)|[-._ ]\[(?<group>[a-z0-9]+)\]$`,
	regexp2.IgnoreCase,
)

// invalidGroupRegex ports InvalidReleaseGroupRegex: a trailing token that
// looks like a group but is actually a season/episode marker or an 8-hex
// scene-obfuscation hash.
var invalidGroupRegex = mustCompile(`^([se]\d+|[0-9a-f]{8})$`, regexp2.IgnoreCase)

// allDigitsRegex is ParseReleaseGroup's int.TryParse rejection: a bare
// number after the last dash is a part or disc number, not a group.
var allDigitsRegex = mustCompile(`^\d+$`, regexp2.None)

// groupFileExtensions are the extensions FileExtensions.RemoveFileExtension
// strips before a group is looked for: media containers plus the usenet
// ".nzb"/".par2" -- nothing else, so a title ending ".Vol.2" or ".DD5.1"
// keeps its tail.
var groupFileExtensions = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".wmv": true, ".mov": true, ".mpg": true,
	".mpeg": true, ".m2ts": true, ".ts": true, ".webm": true, ".flv": true, ".divx": true, ".xvid": true,
	".vob": true, ".ogm": true, ".iso": true, ".img": true, ".strm": true, ".nzb": true, ".par2": true,
}

func removeFileExtension(title string) string {
	if i := strings.LastIndexByte(title, '.'); i >= 0 && groupFileExtensions[strings.ToLower(title[i:])] {
		return title[:i]
	}
	return title
}

// lastGroup returns the named group "group" of re's LAST match in s, or "".
// A match timeout degrades to whatever was found before it, never to an
// error: the group is optional metadata, and ReleaseGroupParser returns null
// for "found nothing" too.
func lastGroup(re *regexp2.Regexp, s string) string {
	last := ""
	for m, err := re.FindStringMatch(s); err == nil && m != nil; m, err = re.FindNextMatch(m) {
		if g := m.GroupByName("group"); g != nil && len(g.Captures) > 0 {
			last = g.String()
		}
	}
	return last
}

func replaceAll(re *regexp2.Regexp, s string) string {
	if out, err := re.Replace(s, "", 0, -1); err == nil {
		return out
	}
	return s
}

// editionRegex ports the token alternation EditionRegex matches against.
var editionRegex = mustCompile(
	`\b(Director'?s[.\s]?Cut|Extended[.\s]?(?:Cut|Edition)?|Theatrical(?:[.\s]Cut)?|Unrated|`+
		`Remastered|IMAX|Uncut|Ultimate[.\s]?(?:Cut|Edition)?|Special[.\s]?Edition)\b`,
	regexp2.IgnoreCase,
)

// editionCanonical normalizes the handful of edition tokens the fixture
// corpus and EditionRegex's alternation exercise to their canonical display
// form. The lookup key strips punctuation/whitespace and lower-cases, so
// "Directors.Cut", "director's cut" and "DIRECTORS CUT" all collapse to the
// same entry.
var editionCanonical = map[string]string{
	"directorscut":    "Director's Cut",
	"extended":        "Extended",
	"extendedcut":     "Extended",
	"extendededition": "Extended",
	"theatrical":      "Theatrical",
	"theatricalcut":   "Theatrical",
	"unrated":         "Unrated",
	"remastered":      "Remastered",
	"imax":            "IMAX",
	"uncut":           "Uncut",
	"ultimate":        "Ultimate",
	"ultimatecut":     "Ultimate",
	"ultimateedition": "Ultimate",
	"specialedition":  "Special Edition",
}

func editionLookupKey(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(raw) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// parseEdition matches editionRegex and normalizes the captured token to its
// canonical form via editionCanonical, falling back to punctuation-to-space
// normalization for a token the table doesn't cover.
func parseEdition(title string) string {
	m, err := editionRegex.FindStringMatch(title)
	if err != nil || m == nil {
		return ""
	}
	raw := m.String()
	if canonical, ok := editionCanonical[editionLookupKey(raw)]; ok {
		return canonical
	}
	return strings.TrimSpace(strings.NewReplacer(".", " ", "_", " ").Replace(raw))
}

// parseGroup extracts the release group, scene-obfuscation hash fallback and
// edition from a release title, following ReleaseGroupParser.ParseReleaseGroup
// step for step: strip a media/usenet extension, a website prefix and a
// public-tracker suffix; take a leading "[SubGroup]" anime group if there is
// one; strip reposter suffixes; then the exact-name exceptions, the
// "(Group)"/"[Group]" exceptions and finally the generic pattern, each
// taking its last match. Only the generic pattern's result is checked
// against invalidGroupRegex, and there Clustarr departs from *arr in one
// way: an 8-hex token is kept as ParsedRelease.Hash rather than discarded.
// A bare number is discarded, as upstream does.
func parseGroup(title string) (group, hash, edition string) {
	edition = parseEdition(title)

	t := removeFileExtension(strings.TrimSpace(title))
	t = replaceAll(websitePrefixRegex, t)
	t = replaceAll(cleanTorrentSuffixRegex, t)

	if m, err := animeGroupRegex.FindStringMatch(t); err == nil && m != nil {
		if g := m.GroupByName("subgroup"); g != nil && len(g.Captures) > 0 {
			return g.String(), "", edition
		}
	}

	t = replaceAll(cleanReleaseGroupRegex, t)

	if g := lastGroup(exceptionGroupExactRegex, t); g != "" {
		return g, "", edition
	}
	if g := lastGroup(exceptionGroupWrappedRegex, t); g != "" {
		return g, "", edition
	}

	candidate := lastGroup(releaseGroupRegex, t)
	switch {
	case candidate == "":
	case matchesAny(candidate, allDigitsRegex):
	case matchesAny(candidate, invalidGroupRegex):
		hash = candidate
	default:
		group = candidate
	}
	return group, hash, edition
}
