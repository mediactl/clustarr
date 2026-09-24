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

package subtitlerequest

import (
	"context"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/providerset"
	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/embedded"
)

// maxExisting is status.existing's +kubebuilder:validation:MaxItems. A file
// with more attributable subtitles than this keeps the first maxExisting in
// [buildExisting]'s order rather than having its whole status apply rejected.
const maxExisting = 64

// Subtitle codec names as ffprobe reports them (research note §3.1) for the
// three kinds the profile's embedded policy can ignore.
const (
	codecPGS    = "hdmv_pgs_subtitle"
	codecVobSub = "dvd_subtitle"
	codecASS    = "ass"
	codecSSA    = "ssa"
)

// normalizeLang maps any language code a probe, a filename or a profile
// carries onto the one BCP-47 form every comparison in this package uses.
//
// This is the load-bearing call of the whole planner. ffprobe reports stream
// languages as ISO 639-2 ("eng", "fre") while profiles and LangKey use
// BCP-47 ("en", "fr"), and subtitles.Plan compares by exact string equality.
// Without this, no embedded subtitle ever counts as existing and no audio
// track ever trips audioExclude, so every file is searched for subtitles it
// already has -- the bug class this project has now hit three times.
func normalizeLang(code string) (string, bool) {
	t, ok := lang.Normalize(code)
	return string(t), ok
}

// normalizeKey re-renders k with its language normalised, keeping its forced
// and hi suffixes.
func normalizeKey(k subtitles.LangKey) (subtitles.LangKey, bool) {
	l, forced, hi, err := subtitles.ParseLangKey(k)
	if err != nil {
		return "", false
	}
	n, ok := normalizeLang(l)
	if !ok {
		return "", false
	}
	return subtitles.FormatLangKey(n, forced, hi), true
}

// ignoredByPolicy reports whether the profile's embedded policy excludes s
// from counting as an existing subtitle: design spec §6.5's "skip bitmap per
// profile and commentary", with the codec split research note §3.1 lists.
// A codec the policy has no knob for counts, as it does in Bazarr.
func ignoredByPolicy(s commonv1alpha1.SubtitleStream, p subtitlev1alpha1.EmbeddedSpec) bool {
	switch codec := strings.ToLower(s.Codec); {
	case p.IgnorePGS && codec == codecPGS:
		return true
	case p.IgnoreVobSub && codec == codecVobSub:
		return true
	case p.IgnoreASS && (codec == codecASS || codec == codecSSA):
		return true
	}
	return p.SkipCommentaryOrDefault() && strings.Contains(strings.ToLower(s.Title), "commentary")
}

// extractors returns the SubtitleProviders the fetch worker would task with
// extracting an embedded track for a profile: the enabled embedded ones,
// narrowed and ordered by the profile's spec.providers exactly as the
// worker narrows its own set (providerset.Order). Only what
// [extractableStreams] reads -- name, type and spec.languages -- is filled.
func extractors(providers []subtitlev1alpha1.SubtitleProvider, profileProviders []string) []providerset.Entry {
	var out []providerset.Entry
	for _, sp := range providers {
		if sp.Spec.Type != subtitlev1alpha1.SubtitleProviderEmbedded || !sp.Spec.EnabledOrDefault() {
			continue
		}
		out = append(out, providerset.Entry{
			Name: sp.Name, Namespace: sp.Namespace, Type: sp.Spec.Type, Languages: slices.Clone(sp.Spec.Languages),
		})
	}
	return providerset.Order(out, profileProviders)
}

// extractableStreams gives spec.embedded.extract its effect. It returns the
// index of every embedded subtitle stream that is written out as a sidecar
// rather than counted as existing: extract is on, the embedded provider
// itself would offer the stream (a text codec, ignoreASS and skipCommentary
// honoured -- asked of pkg/subtitles/providers/embedded's own Search, which
// reads only the probe, so the planner and the extractor cannot disagree on
// which codecs extract), and an extractor the fetch worker would task
// serves its language. Such a stream's language stays wanted, the fetch
// task goes out, and the worker's local tier writes the track out as a
// sidecar before any remote provider is asked (captionarr/worker/fetch).
//
// Design spec §6.5 says "existing = embedded text streams", which left
// extract no effect at all: an embedded track always counted, so its
// language was never wanted and the embedded provider never tasked. The
// SubtitleProfile API documents extract (default true) as "writes a
// matching embedded track out as a sidecar instead of searching providers
// for it", and gap-fix task X11b ruled for the API: with extract on, an
// extractable text track is a want the embedded provider fills; with it off,
// §6.5 holds and the track counts as existing. A bitmap track (PGS,
// VobSub) cannot be extracted as text, so it follows §6.5 either way.
//
// With no extractor -- no enabled embedded SubtitleProvider, one the
// profile's spec.providers leaves out, or one whose spec.languages excludes
// the track's language -- extraction cannot happen, and the track counts as
// existing as before. Treating it as wanted would send every such language
// to the remote providers instead: downloads for subtitles the file already
// carries, on every library that never configured an embedded provider.
func extractableStreams(ctx context.Context, mi *commonv1alpha1.MediaInfo, policy subtitlev1alpha1.EmbeddedSpec,
	extractors []providerset.Entry,
) map[int32]bool {
	if mi == nil || !policy.ExtractOrDefault() || len(extractors) == 0 {
		return nil
	}
	cands, err := embedded.New(embedded.Config{
		Info: *mi, IgnoreASS: policy.IgnoreASS, SkipCommentary: policy.SkipCommentaryOrDefault(),
	}).Search(ctx, subtitles.Query{})
	if err != nil {
		return nil
	}
	out := map[int32]bool{}
	for _, c := range cands {
		idx, err := strconv.ParseInt(c.FetchID, 10, 32)
		if err != nil {
			continue
		}
		l, ok := normalizeLang(c.Language)
		if !ok {
			continue
		}
		if slices.ContainsFunc(extractors, func(e providerset.Entry) bool { return e.Serves([]string{l}) }) {
			out[int32(idx)] = true
		}
	}
	return out
}

// embeddedExisting is the embedded half of status.existing: every subtitle
// stream the probe reported that the policy does not ignore and whose
// language resolves, except one extract turns into a want (extract, from
// [extractableStreams]). A stream tagged "und" or not tagged at all is left
// out -- it satisfies no language this controller can name, and the scanner
// never guesses.
func embeddedExisting(mi *commonv1alpha1.MediaInfo, policy subtitlev1alpha1.EmbeddedSpec, mediaBase string,
	extract map[int32]bool,
) []subtitlev1alpha1.ExistingSub {
	if mi == nil {
		return nil
	}
	var out []subtitlev1alpha1.ExistingSub
	for _, s := range mi.Subtitles {
		if ignoredByPolicy(s, policy) || extract[s.Index] {
			continue
		}
		l, ok := normalizeLang(s.Language)
		if !ok {
			continue
		}
		// FormatLangKey gives forced precedence; a stream flagged both is a
		// forced track (research note §3.2's writing rule).
		key := subtitles.FormatLangKey(l, s.Forced, s.HearingImpaired && !s.Forced)
		idx := s.Index
		out = append(out, subtitlev1alpha1.ExistingSub{
			LangKey:     string(key),
			Source:      subtitlev1alpha1.SubtitleSourceEmbedded,
			Path:        mediaBase,
			StreamIndex: &idx,
		})
	}
	return out
}

// sidecarExisting is the sidecar half of status.existing: every name in the
// media file's directory that subtitles.ParseSidecar attributes to this
// video, right to left. ParseSidecar keeps the language segment as written
// ("Movie.eng.srt" yields "eng"), so the key is normalised here exactly like
// an embedded stream's.
func sidecarExisting(names []string, videoBase string) []subtitlev1alpha1.ExistingSub {
	stem := strings.TrimSuffix(videoBase, filepath.Ext(videoBase))
	var out []subtitlev1alpha1.ExistingSub
	for _, name := range names {
		k, ok := subtitles.ParseSidecar(stem, name)
		if !ok {
			continue
		}
		k, ok = normalizeKey(k)
		if !ok {
			continue
		}
		out = append(out, subtitlev1alpha1.ExistingSub{
			LangKey: string(k),
			Source:  subtitlev1alpha1.SubtitleSourceSidecar,
			Path:    name,
		})
	}
	return out
}

// buildExisting assembles status.existing: embedded streams first, in stream
// order, then sidecars by filename, keeping only subtitles in one of the
// profile's languages (ExistingSub.LangKey is "the profile language key this
// subtitle satisfies") and capped at maxExisting. An embedded stream in
// extract is left out: it satisfies nothing until it is written out.
func buildExisting(mi *commonv1alpha1.MediaInfo, policy subtitlev1alpha1.EmbeddedSpec,
	mediaBase string, dirNames []string, profileLangs map[string]bool, extract map[int32]bool,
) []subtitlev1alpha1.ExistingSub {
	emb := embeddedExisting(mi, policy, mediaBase, extract)
	sort.SliceStable(emb, func(i, j int) bool { return *emb[i].StreamIndex < *emb[j].StreamIndex })
	side := sidecarExisting(dirNames, mediaBase)
	sort.SliceStable(side, func(i, j int) bool { return side[i].Path < side[j].Path })

	var out []subtitlev1alpha1.ExistingSub
	for _, e := range slices.Concat(emb, side) {
		l, _, _, err := subtitles.ParseLangKey(subtitles.LangKey(e.LangKey))
		if err != nil || !profileLangs[l] {
			continue
		}
		if len(out) == maxExisting {
			break
		}
		out = append(out, e)
	}
	return out
}

// existingKeys projects status.existing into subtitles.Plan's input.
func existingKeys(ex []subtitlev1alpha1.ExistingSub) []subtitles.Existing {
	out := make([]subtitles.Existing, 0, len(ex))
	for _, e := range ex {
		out = append(out, subtitles.Existing{LangKey: subtitles.LangKey(e.LangKey)})
	}
	return out
}

// audioLanguages is the file's audio-track languages, normalised, for the
// planner's audioExclude/audioOnlyInclude. An unresolvable tag is dropped:
// "und" audio neither excludes nor includes anything.
func audioLanguages(mi *commonv1alpha1.MediaInfo) []string {
	if mi == nil {
		return nil
	}
	var out []string
	for _, a := range mi.Audio {
		if l, ok := normalizeLang(a.Language); ok && !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out
}

// plannerProfile translates a SubtitleProfileSpec into subtitles.Plan's
// mirror type, restricted to override (SubtitleRequest.spec.languages) when
// that is non-empty. It returns the override keys that name no profile
// language -- the CRD says every entry must be one, and a key that is not is
// reported rather than silently dropped -- and the set of normalised
// languages the result wants, which [buildExisting] filters on.
//
// Every Language goes through [normalizeLang] for the same reason stream
// languages do: the planner compares the two by string equality, and a
// profile is free to say "EN" or "eng" where the probe says "en". Keys are
// kept verbatim -- they are the identity of status.items entries and of
// spec.cutoff, not a language code to compare.
func plannerProfile(spec subtitlev1alpha1.SubtitleProfileSpec, override []string) (subtitles.Profile, []string, map[string]bool) {
	var unknown []string
	for _, k := range override {
		if !slices.ContainsFunc(spec.Languages, func(l subtitlev1alpha1.LanguageItem) bool { return l.Key == k }) {
			unknown = append(unknown, k)
		}
	}

	p := subtitles.Profile{}
	langs := map[string]bool{}
	for _, l := range spec.Languages {
		if len(override) > 0 && !slices.Contains(override, l.Key) {
			continue
		}
		language := l.Language
		if n, ok := normalizeLang(language); ok {
			language = n
		}
		hi := subtitles.HIPolicy(l.HI)
		if hi == "" {
			hi = subtitles.HIPolicyPrefer
		}
		p.Languages = append(p.Languages, subtitles.ProfileLanguage{
			Key:              subtitles.LangKey(l.Key),
			Language:         language,
			Forced:           l.Forced,
			HI:               hi,
			AudioExclude:     l.AudioExclude,
			AudioOnlyInclude: l.AudioOnlyInclude,
		})
		langs[language] = true
	}
	if spec.Cutoff != nil {
		c := subtitles.LangKey(*spec.Cutoff)
		p.Cutoff = &c
	}
	return p, unknown, langs
}

// minScoreFor is the absolute score a first download must reach: the
// request's minScoreOverride when set, else the profile's minScorePercent for
// the file's kind, converted by subtitles.MinScore.
func minScoreFor(kind commonv1alpha1.MediaKind, spec subtitlev1alpha1.SubtitleProfileSpec, override *int32) int32 {
	pct := spec.MinScorePercent.Episode
	if kind == commonv1alpha1.MediaKindMovie {
		pct = spec.MinScorePercent.Movie
	}
	if override != nil {
		pct = *override
	}
	// MaxScore is a few hundred and pct at most 100, so this never overflows.
	return int32(subtitles.MinScore(kind, int(pct)))
}
