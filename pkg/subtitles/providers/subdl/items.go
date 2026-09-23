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

package subdl

import (
	"bytes"
	"encoding/json"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// searchPayload is GET /subtitles' response, with the field names Bazarr's
// subdl.py reads (and its tests' payloads carry): status/success, error,
// subtitles, totalPages and bazarr_policy.
type searchPayload struct {
	Success   json.RawMessage `json:"success"`
	Status    json.RawMessage `json:"status"`
	Error     json.RawMessage `json:"error"`
	Subtitles []item          `json:"subtitles"`
	// TotalPages is how many pages the search has.
	TotalPages flexInt        `json:"totalPages"`
	Policy     *policyPayload `json:"bazarr_policy"`
}

// isError is Bazarr's _is_error_payload: a falsy "success" or "status" key.
func (s searchPayload) isError() bool {
	return present(s.Success) && !truthy(s.Success) || present(s.Status) && !truthy(s.Status)
}

// item is one subtitle in a search response.
type item struct {
	URL          string          `json:"url"`
	Name         string          `json:"name"`
	Language     string          `json:"language"`
	SubtitlePage string          `json:"subtitlePage"`
	Releases     []string        `json:"releases"`
	Author       string          `json:"author"`
	Comment      string          `json:"comment"`
	HI           json.RawMessage `json:"hi"`
	AITranslated json.RawMessage `json:"ai_translated"`
	Season       flexInt         `json:"season"`
	Episode      flexInt         `json:"episode"`
	EpisodeFrom  flexInt         `json:"episode_from"`
	EpisodeEnd   flexInt         `json:"episode_end"`
	FullSeason   json.RawMessage `json:"full_season"`
	UnpackFiles  []unpackFile    `json:"unpack_files"`
}

// unpackFile is one member of a pack the server has already extracted
// (the unpack=1 search parameter): a direct link to that one file.
type unpackFile struct {
	URL     string          `json:"url"`
	FileNID flexString      `json:"file_n_id"`
	Name    string          `json:"name"`
	Season  flexInt         `json:"season"`
	Episode flexInt         `json:"episode"`
	HI      json.RawMessage `json:"hi"`
}

// policyPayload is the small, bounded bazarr_policy object api.subdl.com
// returns to a bazarr=1 search (Bazarr's _apply_bazarr_policy). This client
// does not send bazarr=1; the block is still honoured if it ever arrives.
type policyPayload struct {
	Enabled               *bool           `json:"enabled"`
	SeasonFallbackEnabled *bool           `json:"season_fallback_enabled"`
	TitleFallbackEnabled  *bool           `json:"title_fallback_enabled"`
	UnpackEnabled         *bool           `json:"unpack_enabled"`
	MaxPages              json.RawMessage `json:"max_pages"`
}

// flexInt decodes a number, a numeric string or null. SubDL sends 0 or
// null to mean "not applicable" (Bazarr's _episode_number), and a field
// that arrives as some other type is not worth failing a search over.
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	*f = 0
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		if i, err := n.Int64(); err == nil {
			*f = flexInt(i)
		}
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		if i, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			*f = flexInt(i)
		}
	}
	return nil
}

// positive is Bazarr's _episode_number: the value, or 0 when it is not a
// usable positive number.
func (f flexInt) positive() int { return max(int(f), 0) }

// flexString decodes a string or a number as its text.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexString(s)
		return nil
	}
	*f = flexString(strings.Trim(string(bytes.TrimSpace(b)), `"`))
	if *f == "null" {
		*f = ""
	}
	return nil
}

func present(raw json.RawMessage) bool { return len(bytes.TrimSpace(raw)) > 0 }

// truthy is Python truthiness for a JSON value.
func truthy(raw json.RawMessage) bool {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t != ""
	case nil:
		return false
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	}
	return false
}

// Bazarr's HI and forced heuristics (subdl.py _HI_WORD_RE, _NON_HI_RE,
// _FORCED_RE). Python's (?<![a-z0-9]) and (?![a-z0-9]) lookarounds become
// (?:^|[^a-z0-9]) and (?:[^a-z0-9]|$): RE2 has no lookaround, and for a
// yes/no search the two are the same. Under (?i) the negated class
// excludes upper case too, as Python's IGNORECASE does -- so "Hive-CM8",
// "Hi10P" and "Hindi" are not hearing-impaired.
var (
	hiWordRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:hi|sdh|cc)(?:[^a-z0-9]|$)|_hi_|𝓢𝓓𝓗|hearing[\s._-]?impaired|closed[\s._-]?caption`)
	nonHIRe  = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:non[\s._-]?(?:hi|sdh)|(?:hi|sdh)[\s._-]?removed?|removed?[\s._-]?(?:hi|sdh))(?:[^a-z0-9]|$)`)
	forcedRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:forced|foreign[\s._-]?parts?)(?:[^a-z0-9]|$)`)
)

// texts is Bazarr's _item_texts: the comment, the archive name and every
// release name.
func (it item) texts() []string {
	return append([]string{it.Comment, it.Name}, it.Releases...)
}

func anyMatch(re *regexp.Regexp, texts []string) bool {
	return slices.ContainsFunc(texts, re.MatchString)
}

// hearingImpaired is Bazarr's _is_hearing_impaired: an explicit non-HI
// marker wins over everything; then the API's own hi flag; then the text
// heuristic.
func (it item) hearingImpaired() bool {
	if anyMatch(nonHIRe, it.texts()) {
		return false
	}
	return truthy(it.HI) || anyMatch(hiWordRe, it.texts())
}

// forced is Bazarr's _is_forced: a forced or foreign-parts marker in the
// comment, the name or a release name.
func (it item) forced() bool { return anyMatch(forcedRe, it.texts()) }

// episodeRangeFromReleases is Bazarr's _parse_episode_range_from_releases:
// the first release name that spells an episode range gives the pack's
// first and last episode.
func episodeRangeFromReleases(releases []string) (from, end int) {
	for _, name := range releases {
		p, err := release.Parse(name, release.Options{Kind: common.MediaKindEpisode})
		if err == nil && len(p.Episodes) >= 2 {
			return p.Episodes[0], p.Episodes[len(p.Episodes)-1]
		}
	}
	return 0, 0
}

// singleEpisode is Bazarr's _guessit_single_episode: the one episode a file
// name spells, or 0 for a name that spells none -- or a range, which is not
// a statement about one episode.
func singleEpisode(name string) int {
	if name == "" {
		return 0
	}
	stem := strings.TrimSuffix(path.Base(name), path.Ext(name))
	p, err := release.Parse(stem, release.Options{Kind: common.MediaKindEpisode})
	if err != nil || len(p.Episodes) != 1 {
		return 0
	}
	return p.Episodes[0]
}

// selectUnpackEntry is Bazarr's _select_unpack_entry: pick the pack member
// the server already extracted for the target episode. The server's
// per-file episode numbers are parsed from file names and have been
// measured wrong on about 3.6% of packs, so they are cross-checked against
// the name:
//
//	rank 0: the name and the server both say the target;
//	rank 1: the name says the target, the server says nothing (or 0);
//	rank 2: the server says the target and the name is silent;
//	never:  the name contradicts the server, or neither says the target.
//
// Within a rank a member whose HI flag matches the pack's is preferred, so
// a non-HI profile is not handed the SDH member of a mixed pack.
func selectUnpackEntry(files []unpackFile, target int, preferHI bool) (unpackFile, bool) {
	type ranked struct {
		rank, hiMismatch, index int
		f                       unpackFile
	}
	var cands []ranked
	for i, f := range files {
		server := f.Episode.positive()
		claimed := server == target
		named := singleEpisode(f.Name)
		var rank int
		switch {
		case named == target:
			rank = 1
			if claimed {
				rank = 0
			}
		case claimed && named == 0:
			rank = 2
		default:
			continue
		}
		mismatch := 1
		if truthy(f.HI) == preferHI {
			mismatch = 0
		}
		cands = append(cands, ranked{rank, mismatch, i, f})
	}
	if len(cands) == 0 {
		return unpackFile{}, false
	}
	slices.SortFunc(cands, func(a, b ranked) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		if a.hiMismatch != b.hiMismatch {
			return a.hiMismatch - b.hiMismatch
		}
		return a.index - b.index
	})
	return cands[0].f, true
}

// unsafeTitleChars is Bazarr's UNSAFE_TITLE_CHARS: the API rejects a
// film_name containing any of them ("Film name contains potentially unsafe
// characters").
var unsafeTitleChars = regexp.MustCompile("[<>{}\\[\\]'\"`´’;\\\\/]")

// sanitizeTitle is Bazarr's _sanitize_title: every unsafe character becomes
// a space, never nothing -- the index stores "Grey's Anatomy" as grey|s|
// anatomy, so "Greys" would match nothing -- and runs of space collapse.
func sanitizeTitle(title string) string {
	return strings.Join(strings.Fields(unsafeTitleChars.ReplaceAllString(title, " ")), " ")
}
