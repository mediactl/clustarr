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

package fetch

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dlclark/regexp2"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// regexTimeout bounds one mustContain/mustNotContain match. The patterns are
// user-supplied and regexp2 backtracks, so an unbounded one could hang a
// worker on a pathological release_info string.
const regexTimeout = 100 * time.Millisecond

// want is the one profile language a fetch task is for, resolved from the
// SubtitleProfile entry whose key the task names.
type want struct {
	key    subtitles.LangKey
	lang   string // BCP-47, from LanguageItem.Language
	forced bool
	hi     subtitlev1alpha1.HIPolicy

	// accept is the set of canonical language tags a candidate may carry to
	// satisfy this entry: the entry's own language plus every
	// spec.languageEquals alias of it.
	accept map[string]bool
}

// tags returns the acceptable tags, for matching SubtitleProvider
// spec.languages.
func (w want) tags() []string {
	out := make([]string, 0, len(w.accept))
	for t := range w.accept {
		out = append(out, t)
	}
	slices.Sort(out)
	return out
}

// wantFor finds the profile entry keyed langKey. ok is false when the
// profile no longer has such an entry, or the request's spec.languages
// override excludes it -- a task published before a profile edit, which
// the controller's replan supersedes.
func wantFor(profile *subtitlev1alpha1.SubtitleProfile, override []string, langKey string) (want, bool) {
	if len(override) > 0 && !slices.Contains(override, langKey) {
		return want{}, false
	}
	for _, l := range profile.Spec.Languages {
		if l.Key != langKey {
			continue
		}
		w := want{
			key:    subtitles.LangKey(l.Key),
			lang:   l.Language,
			forced: l.Forced,
			hi:     l.HI,
			accept: map[string]bool{canonical(l.Language): true},
		}
		// spec.languageEquals: "<from>:<to>" makes the two interchangeable.
		for _, pair := range profile.Spec.LanguageEquals {
			from, to, ok := strings.Cut(pair, ":")
			if !ok {
				continue
			}
			switch canonical(l.Language) {
			case canonical(from):
				w.accept[canonical(to)] = true
			case canonical(to):
				w.accept[canonical(from)] = true
			}
		}
		return w, true
	}
	return want{}, false
}

// canonical normalises a language tag for comparison. A tag pkg/lang cannot
// resolve is compared verbatim (lower-cased), never mapped to a guess.
func canonical(tag string) string {
	if t, ok := lang.Normalize(tag); ok {
		return string(t)
	}
	return strings.ToLower(tag)
}

// filters are the profile- and provider-level candidate filters: Bazarr's
// _Banlist (mustContain / mustNotContain on release_info) plus the
// provider's trusted-source and machine-translation options.
type filters struct {
	mustContain, mustNotContain []*regexp2.Regexp
	requireTrusted              bool
	allowAITranslated           bool
}

// compileFilters compiles the profile's regexes with regexp2 (CLAUDE.md:
// Bazarr patterns are Python regexes, which RE2 rejects when they
// backtrack), case-insensitive and with a match timeout. A pattern that does
// not compile is an invalid profile, reported to the caller.
func compileFilters(spec subtitlev1alpha1.SubtitleProfileSpec) (filters, error) {
	var f filters
	for _, p := range spec.MustContain {
		re, err := regexp2.Compile(p, regexp2.IgnoreCase)
		if err != nil {
			return filters{}, fmt.Errorf("mustContain %q: %w", p, err)
		}
		re.MatchTimeout = regexTimeout
		f.mustContain = append(f.mustContain, re)
	}
	for _, p := range spec.MustNotContain {
		re, err := regexp2.Compile(p, regexp2.IgnoreCase)
		if err != nil {
			return filters{}, fmt.Errorf("mustNotContain %q: %w", p, err)
		}
		re.MatchTimeout = regexTimeout
		f.mustNotContain = append(f.mustNotContain, re)
	}
	return f, nil
}

// withProviderOptions returns f with one SubtitleProvider's spec.options
// applied: trustedSources=true keeps only trusted uploads, and
// machine-translated or AI-translated subtitles are dropped unless
// aiTranslated=include (Bazarr's OpenSubtitles.com defaults exclude both).
func (f filters) withProviderOptions(opts map[string]string) filters {
	out := f
	out.requireTrusted = strings.EqualFold(opts[subtitlev1alpha1.ProviderOptionTrustedSources], "true")
	out.allowAITranslated = strings.EqualFold(opts[subtitlev1alpha1.ProviderOptionAITranslated], "include")
	return out
}

// reject returns why c cannot satisfy w, or "" when it can. hiVerifiable is
// the provider's own claim that its HI flag is trustworthy: Bazarr only
// enforces a forced-HI or forced-non-HI policy against such a provider,
// since an unverifiable flag would reject good subtitles on noise.
func reject(c subtitles.Candidate, w want, f filters, hiVerifiable bool) string {
	tag, ok := lang.Normalize(c.Language)
	if !ok {
		return fmt.Sprintf("language %q is not recognised", c.Language)
	}
	if !w.accept[string(tag)] {
		return fmt.Sprintf("language %s is not %s", tag, w.lang)
	}
	if c.Forced != w.forced {
		return "forced flag does not match"
	}
	if hiVerifiable {
		switch w.hi {
		case subtitlev1alpha1.HIPolicyRequired:
			if !c.HI {
				return "not hearing-impaired"
			}
		case subtitlev1alpha1.HIPolicyExcluded:
			if c.HI {
				return "hearing-impaired"
			}
		}
	}
	if (c.AITranslated || c.MachineTranslated) && !f.allowAITranslated {
		return "machine translated"
	}
	if f.requireTrusted && !c.Trusted {
		return "not from a trusted source"
	}
	for _, re := range f.mustContain {
		if m, err := re.MatchString(c.ReleaseInfo); err != nil || !m {
			return "release info misses a mustContain pattern"
		}
	}
	for _, re := range f.mustNotContain {
		// A timed-out match is treated as a hit: rejecting on uncertainty
		// is the conservative side of a ban list.
		if m, err := re.MatchString(c.ReleaseInfo); err != nil || m {
			return "release info matches a mustNotContain pattern"
		}
	}
	return ""
}

// identityMatches are the matches a provider's search establishes by
// construction, because it searched by the item's own external id rather
// than by text, and that the provider's client does not already put on its
// candidates. Bazarr's providers add these in get_matches, and without them
// no non-hash candidate could ever reach the default minimum score (70% of
// a movie is 126 points; source, edition and release group together are
// 75).
//
// Most clients now report their own (Candidate.Matches, which [rank]
// OR-merges in): opensubtitlescom from each result's feature_details (series,
// season, episode, title and year, since gap-fix X11a), subdl and subsource
// from how the title was found. None of those gets a rule here: a rule keyed
// on the query alone would over-claim -- for a film-name or text-search
// result, or a result whose ids disagree with the query's. What is left is
// the one client that reports none:
//
//   - gestdown, episode: the client resolves the TVDB id to its show and
//     asks for that season and episode: series, year, season, episode.
//   - embedded: nothing extra; its own hash match already says "this file".
func identityMatches(t subtitlev1alpha1.SubtitleProviderType, kind commonv1.MediaKind, q subtitles.Query) map[string]bool {
	if t == subtitlev1alpha1.SubtitleProviderGestdown && kind == commonv1.MediaKindEpisode && q.IDs["tvdb"] != "" {
		return map[string]bool{
			subtitles.MatchSeries: true, subtitles.MatchYear: true,
			subtitles.MatchSeason: true, subtitles.MatchEpisode: true,
		}
	}
	return nil
}

// searchable reports whether a provider of type t can identify this item at
// all, from what its client really searches on. A search that cannot be
// pinned to the item returns other titles' subtitles -- an OpenSubtitles.com
// query with neither an id nor a hash is a bare language filter -- so it is
// skipped rather than made and scored.
//
//   - opensubtitlescom: a movie by its moviehash, imdb or tmdb id; an
//     episode by its moviehash or its SHOW's ids, parent_imdb/parent_tmdb
//     (the catalog has no episode ids; the client sends the show's as
//     parent_imdb_id/parent_tmdb_id with the season and episode numbers).
//   - gestdown: episodes only, by the show's TVDB id.
//   - subdl: a movie by imdb, tmdb or title (film_name); an episode by the
//     show's parent_imdb or its title.
//   - subsource: a movie by imdb; an episode by the show's parent_imdb. The
//     client looks titles up only from an IMDb id.
//   - embedded: always; it reads the file itself.
func searchable(t subtitlev1alpha1.SubtitleProviderType, kind commonv1.MediaKind, q subtitles.Query) bool {
	movie := kind == commonv1.MediaKindMovie
	switch t {
	case subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom:
		if movie {
			return q.Hash != "" || q.IDs["imdb"] != "" || q.IDs["tmdb"] != ""
		}
		return q.Hash != "" || q.IDs["parent_imdb"] != "" || q.IDs["parent_tmdb"] != ""
	case subtitlev1alpha1.SubtitleProviderGestdown:
		return kind == commonv1.MediaKindEpisode && q.IDs["tvdb"] != ""
	case subtitlev1alpha1.SubtitleProviderSubDL:
		if movie {
			return q.IDs["imdb"] != "" || q.IDs["tmdb"] != "" || q.Title != ""
		}
		return q.IDs["parent_imdb"] != "" || q.Title != ""
	case subtitlev1alpha1.SubtitleProviderSubSource:
		if movie {
			return q.IDs["imdb"] != ""
		}
		return q.IDs["parent_imdb"] != ""
	}
	return true
}

// ranked is one candidate that passed the filters, with its score.
type ranked struct {
	c              subtitles.Candidate
	score, without int
}

// rankInput is everything [rank] needs about one provider's search.
type rankInput struct {
	kind           commonv1.MediaKind
	providerType   subtitlev1alpha1.SubtitleProviderType
	hashVerifiable bool
	hiVerifiable   bool
	query          subtitles.Query
	want           want
	filters        filters
	threshold      int
}

// rankResult is what [rank] made of one provider's candidates.
type rankResult struct {
	// accepted are the candidates at or above the threshold, best first.
	accepted []ranked
	// bestBelow is the highest score among candidates that passed the
	// filters but not the threshold, for the "why nothing" message.
	bestBelow int
	// filtered counts candidates rejected by [reject].
	filtered int
}

// rank is compute_score plus download_best_subtitles' ordering (research
// note §5): filter, merge the provider's own matches with the release-derived
// ones and the identity ones, apply the hash-corroboration and HI-bonus
// rules, score, drop below the threshold, and sort by (score,
// score-without-hash) descending. Downloads and then the candidate id break
// ties so the order is deterministic.
func rank(in rankInput, cands []subtitles.Candidate) rankResult {
	var out rankResult
	isSpecial := in.kind == commonv1.MediaKindEpisode && in.query.Season == 0
	identity := identityMatches(in.providerType, in.kind, in.query)
	for _, c := range cands {
		if reject(c, in.want, in.filters, in.hiVerifiable) != "" {
			out.filtered++
			continue
		}
		raw := map[string]bool{}
		for k, v := range c.Matches {
			raw[k] = raw[k] || v
		}
		for k, v := range subtitles.GuessMatches(in.kind, in.query.Release, c.ReleaseInfo) {
			raw[k] = raw[k] || v
		}
		for k, v := range identity {
			raw[k] = raw[k] || v
		}
		matches := subtitles.CandidateMatches(in.kind, in.hashVerifiable, isSpecial, hiMatch(in.want.hi, c.HI), raw)
		score, without := subtitles.Score(in.kind, matches)
		if score < in.threshold {
			out.bestBelow = max(out.bestBelow, score)
			continue
		}
		c.Matches = matches
		c.Score, c.ScoreWithoutHash = score, without
		out.accepted = append(out.accepted, ranked{c: c, score: score, without: without})
	}
	slices.SortStableFunc(out.accepted, func(a, b ranked) int {
		return cmp.Or(
			cmp.Compare(b.score, a.score),
			cmp.Compare(b.without, a.without),
			cmp.Compare(b.c.Downloads, a.c.Downloads),
			cmp.Compare(subtitleID(a.c), subtitleID(b.c)),
		)
	})
	return out
}

// hiMatch is CandidateMatches' wantHIMatch argument: whether the candidate's
// HI flag agrees with a required or excluded policy, or nil under "prefer",
// which accepts either without a bonus (research note §5 item 2).
func hiMatch(policy subtitlev1alpha1.HIPolicy, candidateHI bool) *bool {
	var m bool
	switch policy {
	case subtitlev1alpha1.HIPolicyRequired:
		m = candidateHI
	case subtitlev1alpha1.HIPolicyExcluded:
		m = !candidateHI
	default:
		return nil
	}
	return &m
}

// subtitleID is the provider-scoped id recorded in items[].subtitleID: the
// candidate's ID where the provider has one (Gestdown), else its fetch
// handle (an OpenSubtitles file_id, an embedded stream index).
func subtitleID(c subtitles.Candidate) string {
	id := c.ID
	if id == "" {
		id = c.FetchID
	}
	return truncate(id, maxSubtitleID)
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence, for
// the CRD's MaxLength limits.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !validTail(s) {
		s = s[:len(s)-1]
	}
	return s
}

// validTail reports whether s does not end in a partial UTF-8 sequence.
func validTail(s string) bool {
	return strings.ToValidUTF8(s, "�") == s
}
