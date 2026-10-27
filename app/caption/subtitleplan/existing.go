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

package subtitleplan

import (
	"context"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/providerset"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/embedded"
)

// Subtitle codec names as ffprobe reports them (research note §3.1) for the
// three kinds the profile's embedded policy can ignore.
const (
	codecPGS    = "hdmv_pgs_subtitle"
	codecVobSub = "dvd_subtitle"
	codecASS    = "ass"
	codecSSA    = "ssa"
)

// existingSub is one subtitle that already satisfies a profile language:
// an embedded stream (embedded, streamIndex) or a sidecar (name).
type existingSub struct {
	langKey     string
	name        string
	embedded    bool
	streamIndex int32
}

// normalizeLang maps any language code a probe, a filename or a profile
// carries onto the one BCP-47 form every comparison in this package uses.
//
// This is the load-bearing call of the whole planner. ffprobe reports stream
// languages as ISO 639-2 ("eng", "fre") while profiles and LangKey use
// BCP-47 ("en", "fr"), and subtitles.Plan compares by exact string equality.
// Without this, no embedded subtitle ever counts as existing and no audio
// track ever trips audioExclude, so every file is searched for subtitles it
// already has.
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
// from counting as an existing subtitle: bitmap codecs per profile and
// commentary. A codec the policy has no knob for counts, as it does in
// Bazarr.
func ignoredByPolicy(s commonv1.SubtitleStream, p subtitlev1alpha1.EmbeddedSpec) bool {
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

// EmbeddedEntries returns the SubtitleProviders the fetch worker could task
// with extracting an embedded track: the enabled embedded ones, unordered
// (Plan orders them by the winning profile's spec.providers, as the worker
// does). Only what extractableStreams reads -- name, type and
// spec.languages -- is filled.
func EmbeddedEntries(providers []subtitlev1alpha1.SubtitleProvider) []providerset.Entry {
	var out []providerset.Entry
	for _, sp := range providers {
		if sp.Spec.Type != subtitlev1alpha1.SubtitleProviderEmbedded || !sp.Spec.EnabledOrDefault() {
			continue
		}
		out = append(out, providerset.Entry{
			Name: sp.Name, Namespace: sp.Namespace, Type: sp.Spec.Type, Languages: slices.Clone(sp.Spec.Languages),
		})
	}
	return out
}

// OCREntries returns the SubtitleProviders the fetch worker could task with
// reading an image sidecar: the enabled ocr ones, unordered (Plan orders
// them by the winning profile's spec.providers, as the worker does). Only
// what imageWants reads -- name, type and spec.languages -- is filled.
func OCREntries(providers []subtitlev1alpha1.SubtitleProvider) []providerset.Entry {
	var out []providerset.Entry
	for _, sp := range providers {
		if sp.Spec.Type != subtitlev1alpha1.SubtitleProviderOCR || !sp.Spec.EnabledOrDefault() {
			continue
		}
		out = append(out, providerset.Entry{
			Name: sp.Name, Namespace: sp.Namespace, Type: sp.Spec.Type, Languages: slices.Clone(sp.Spec.Languages),
		})
	}
	return out
}

// imageSidecar is one image subtitle beside the file -- a .sup the MP4
// standard wrote for a PGS track no text track covers (standard.Version 8)
// -- with the language its name carries. A bare <stem>.sup, an untagged
// track's, is attributed to the item's original language, as an untagged
// audio track is (audioLanguages): the three such files of The Sopranos'
// first season were English; "" attributes nothing.
type imageSidecar struct {
	name       string
	lang       string
	forced, hi bool
}

func (s imageSidecar) key() subtitles.LangKey { return subtitles.FormatLangKey(s.lang, s.forced, s.hi) }

// imageSidecars is the image sidecars among names that belong to the video,
// tagged ones by their key and bare ones by originalLanguage.
func imageSidecars(videoBase string, names []string, originalLanguage string) []imageSidecar {
	stem := strings.TrimSuffix(videoBase, filepath.Ext(videoBase))
	var out []imageSidecar
	for _, name := range names {
		if !subtitles.ImageSidecar(name) {
			continue
		}
		if k, ok := subtitles.ParseSidecar(stem, name); ok {
			if k, ok = normalizeKey(k); ok {
				l, forced, hi, _ := subtitles.ParseLangKey(k)
				out = append(out, imageSidecar{name: name, lang: l, forced: forced, hi: hi})
			}
			continue
		}
		if subtitles.BareSidecar(stem, name) {
			if l, ok := normalizeLang(originalLanguage); ok {
				out = append(out, imageSidecar{name: name, lang: l})
			}
		}
	}
	return out
}

// imageWants is the owner's "convert .sup to .srt when .sup is the only
// subtitle format provided in the blu-ray" (2026-10-27): a profile language
// that has an image sidecar beside the file of its language and forced
// flag (and a hearing-impaired flag its policy admits), no text subtitle,
// and an ocr provider that serves it, is wanted whatever the profile's
// audio rules say -- the owner's english profile excludes English-audio
// files, and the disc's own English PGS track is still worth reading. It
// returns the keys wanted beyond wanted, which the task marks LocalOnly:
// nothing but the file's own subtitles is asked for them. The cutoff is
// judged without the audio rules too, so a language whose text subtitle
// exists wants nothing.
func imageWants(pp subtitles.Profile, existing []subtitles.Existing, wanted []subtitles.LangKey,
	images []imageSidecar, readers []providerset.Entry,
) []subtitles.LangKey {
	if len(images) == 0 || len(readers) == 0 {
		return nil
	}
	noAudio := pp
	noAudio.Languages = slices.Clone(pp.Languages)
	for i := range noAudio.Languages {
		noAudio.Languages[i].AudioExclude, noAudio.Languages[i].AudioOnlyInclude = false, false
	}
	fromImages, _ := subtitles.Plan(noAudio, nil, existing)
	var out []subtitles.LangKey
	for _, k := range fromImages {
		if slices.Contains(wanted, k) {
			continue
		}
		l := profileLanguage(pp, k)
		if l == nil || !slices.ContainsFunc(images, func(s imageSidecar) bool { return imageSatisfies(*l, s) }) {
			continue
		}
		if !slices.ContainsFunc(readers, func(e providerset.Entry) bool { return e.Serves([]string{l.Language}) }) {
			continue
		}
		out = append(out, k)
	}
	return out
}

// profileLanguage is pp's entry keyed k.
func profileLanguage(pp subtitles.Profile, k subtitles.LangKey) *subtitles.ProfileLanguage {
	for i := range pp.Languages {
		if pp.Languages[i].Key == k {
			return &pp.Languages[i]
		}
	}
	return nil
}

// imageSatisfies reports whether s could be read as l: the same language
// and forced flag (the fetch worker's filter refuses a forced candidate
// for a plain want and the reverse), and a hearing-impaired flag the
// policy admits (required wants one, excluded refuses one, prefer takes
// either).
func imageSatisfies(l subtitles.ProfileLanguage, s imageSidecar) bool {
	if s.lang != l.Language || s.forced != l.Forced {
		return false
	}
	switch l.HI {
	case subtitles.HIPolicyRequired:
		return s.hi
	case subtitles.HIPolicyExcluded:
		return !s.hi
	}
	return true
}

// imageSidecarsFor is the task's ImageSidecars: every image sidecar of the
// language l, whatever its flags; the worker's filter chooses.
func imageSidecarsFor(images []imageSidecar, lang string) []schema.ImageSidecar {
	var out []schema.ImageSidecar
	for _, s := range images {
		if s.lang == lang {
			out = append(out, schema.ImageSidecar{Name: s.name, LangKey: string(s.key())})
		}
	}
	return out
}

// extractableStreams gives spec.embedded.extract its effect. It returns the
// index of every embedded subtitle stream that is written out as a sidecar
// rather than counted as existing: extract is on, the embedded provider
// itself would offer the stream (a text codec, ignoreASS and skipCommentary
// honoured -- asked of pkg/subtitles/providers/embedded's own Search, which
// reads only the probe, so the planner and the extractor cannot disagree on
// which codecs extract), and an extractor the fetch worker would task
// serves its language. Such a stream's language stays wanted, the fetch
// task goes out carrying it in Extract, and the worker's local tier writes
// the track out as a sidecar before any remote provider is asked.
//
// With no extractor -- no enabled embedded SubtitleProvider, one the
// profile's spec.providers leaves out, or one whose spec.languages excludes
// the track's language -- extraction cannot happen, and the track counts as
// existing.
func extractableStreams(mi *commonv1.MediaInfo, policy subtitlev1alpha1.EmbeddedSpec, extractors []providerset.Entry) map[int32]bool {
	if mi == nil || !policy.ExtractOrDefault() || len(extractors) == 0 {
		return nil
	}
	cands, err := embedded.New(embedded.Config{
		Info: *mi, IgnoreASS: policy.IgnoreASS, SkipCommentary: policy.SkipCommentaryOrDefault(),
	}).Search(context.Background(), subtitles.Query{})
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

// embeddedExisting is the embedded half of what exists: every subtitle
// stream the probe reported that the policy does not ignore and whose
// language resolves, except one extract turns into a want. A stream tagged
// "und" or not tagged at all is left out -- it satisfies no language the
// planner can name, and the scanner never guesses.
func embeddedExisting(mi *commonv1.MediaInfo, policy subtitlev1alpha1.EmbeddedSpec, mediaBase string,
	extract map[int32]bool,
) []existingSub {
	if mi == nil {
		return nil
	}
	var out []existingSub
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
		out = append(out, existingSub{langKey: string(key), name: mediaBase, embedded: true, streamIndex: s.Index})
	}
	return out
}

// sidecarExisting is the sidecar half of what exists: every name in the
// media file's directory that subtitles.ParseSidecar attributes to this
// video, right to left. ParseSidecar keeps the language segment as written
// ("Movie.eng.srt" yields "eng"), so the key is normalised here exactly like
// an embedded stream's.
func sidecarExisting(names []string, videoBase string) []existingSub {
	stem := strings.TrimSuffix(videoBase, filepath.Ext(videoBase))
	var out []existingSub
	for _, name := range names {
		if subtitles.ImageSidecar(name) {
			// A .sup the transcode kept beside its MP4 (standard.Version 8)
			// is a subtitle no player reads: the language still wants a
			// text one.
			continue
		}
		k, ok := subtitles.ParseSidecar(stem, name)
		if !ok {
			continue
		}
		k, ok = normalizeKey(k)
		if !ok {
			continue
		}
		out = append(out, existingSub{langKey: string(k), name: name})
	}
	return out
}

// buildExisting assembles what exists: embedded streams first, in stream
// order, then sidecars by filename, keeping only subtitles in one of the
// profile's languages. An embedded stream in extract is left out: it
// satisfies nothing until it is written out. Nothing is stored, so the old
// 64-entry cap is gone (§6.4.3).
func buildExisting(mi *commonv1.MediaInfo, policy subtitlev1alpha1.EmbeddedSpec,
	mediaBase string, dirNames []string, profileLangs map[string]bool, extract map[int32]bool,
) []existingSub {
	emb := embeddedExisting(mi, policy, mediaBase, extract)
	sort.SliceStable(emb, func(i, j int) bool { return emb[i].streamIndex < emb[j].streamIndex })
	side := sidecarExisting(dirNames, mediaBase)
	sort.SliceStable(side, func(i, j int) bool { return side[i].name < side[j].name })

	var out []existingSub
	for _, e := range slices.Concat(emb, side) {
		l, _, _, err := subtitles.ParseLangKey(subtitles.LangKey(e.langKey))
		if err != nil || !profileLangs[l] {
			continue
		}
		out = append(out, e)
	}
	return out
}

// existingKeys projects what exists into subtitles.Plan's input.
func existingKeys(ex []existingSub) []subtitles.Existing {
	out := make([]subtitles.Existing, 0, len(ex))
	for _, e := range ex {
		out = append(out, subtitles.Existing{LangKey: subtitles.LangKey(e.langKey)})
	}
	return out
}

// audioLanguages is the file's audio-track languages, normalised, for the
// planner's audioExclude/audioOnlyInclude. An unresolvable tag is dropped.
// A file whose audio names no language at all -- untagged, "und", or never
// probed -- is taken to be in originalLanguage, its item's original
// language from the metadata provider (the Movie's, or the Episode's
// Series'): 2,571 of the owner's 13,754 video files carry no usable audio
// tag (2026-09-30), and without this an English film among them wanted
// English subtitles under audioExclude. A tagged track always wins over
// the metadata; "" assumes nothing.
func audioLanguages(mi *commonv1.MediaInfo, originalLanguage string) []string {
	var out []string
	if mi != nil {
		for _, a := range mi.Audio {
			if l, ok := normalizeLang(a.Language); ok && !slices.Contains(out, l) {
				out = append(out, l)
			}
		}
	}
	if len(out) == 0 {
		if l, ok := normalizeLang(originalLanguage); ok {
			out = append(out, l)
		}
	}
	return out
}

// plannerProfile translates a SubtitleProfileSpec into subtitles.Plan's
// mirror type, and returns the set of normalised languages it wants, which
// buildExisting filters on.
//
// Every Language goes through normalizeLang for the same reason stream
// languages do: the planner compares the two by string equality, and a
// profile is free to say "EN" or "eng" where the probe says "en". Keys are
// kept verbatim -- they are the identity of status.subtitles.items entries
// and of spec.cutoff, not a language code to compare.
func plannerProfile(spec subtitlev1alpha1.SubtitleProfileSpec) (subtitles.Profile, map[string]bool) {
	p := subtitles.Profile{}
	langs := map[string]bool{}
	for _, l := range spec.Languages {
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
	return p, langs
}

// minScoreFor is the absolute score a first download must reach: the
// profile's minScorePercent for the file's kind, converted by
// subtitles.MinScore.
func minScoreFor(kind commonv1.MediaKind, spec subtitlev1alpha1.SubtitleProfileSpec) int32 {
	pct := spec.MinScorePercent.Episode
	if kind == commonv1.MediaKindMovie {
		pct = spec.MinScorePercent.Movie
	}
	// MaxScore is a few hundred and pct at most 100, so this never overflows.
	return int32(subtitles.MinScore(kind, int(pct))) //nolint:gosec // bounded by MaxScore
}
