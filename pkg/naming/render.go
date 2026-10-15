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

package naming

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/lang"
)

var tokenRe = regexp.MustCompile(`\{([^{}]*)\}`)

func (e Engine) Render(tmpl string, c Context) (string, error) {
	// A multi-episode file's season-episode pattern and absolute numbers are
	// expanded first, as whole patterns (multiepisode.go).
	tmpl = e.expandMultiEpisode(tmpl, c)
	var errOut error
	out := tokenRe.ReplaceAllStringFunc(tmpl, func(raw string) string {
		if errOut != nil {
			return raw
		}
		prefix, name, suffix := splitWrapper(raw[1 : len(raw)-1])
		base, pad, trunc, filter := splitModifier(name)
		key := normalizeTokenName(base)

		var val string
		languages, isLanguages := languageTokens[key]
		switch {
		// episoderange (and Step 8's absoluterange) need e.Config, which a
		// package-level tokenFuncs closure cannot reach -- see these two
		// cases handled specially here instead of rebuilding the whole map
		// per Engine on every Render call.
		case key == "episoderange" && filter == "":
			val = formatEpisodeRange(c.Season, c.Episodes, e.Config.MultiEpisodeStyle.orDefault())
		case key == "absoluterange" && filter == "":
			val = formatAbsoluteRange(c.Absolute, e.Config.MultiEpisodeStyle.orDefault())
		case isLanguages:
			val = languages(c, filter)
		default:
			entry, ok := tokenFuncs[key]
			if !ok || filter != "" {
				// Only a language token takes a filter; anything else
				// after a colon is no token, as it always was.
				if filter != "" {
					base = name
				}
				errOut = fmt.Errorf("%w: %q", ErrUnknownToken, base)
				return raw
			}
			val = entry.fn(c, pad, trunc)
			if entry.colonSensitive {
				val = replaceColon(val, e.Config.ColonReplacement)
			}
		}
		val = replacePathSeparators(val)
		if val == "" {
			return ""
		}
		return prefix + val + suffix
	})
	if errOut != nil {
		return "", errOut
	}
	out = dropEmptySegments(out)
	// Lidarr/Readarr-style global post-processing: replaceSpaces/separator,
	// then an overall case transform, both applied to the fully rendered
	// path, not per-token.
	if e.Config.ReplaceSpaces && e.Config.Separator != "" {
		out = strings.ReplaceAll(out, " ", e.Config.Separator)
	}
	switch e.Config.Case {
	case "upper":
		out = strings.ToUpper(out)
	case "lower":
		out = strings.ToLower(out)
	}
	if err := checkComponents(out); err != nil {
		return "", err
	}
	return out, nil
}

// pathSeparatorReplacer is the *arrs' FileNameBuilder.CleanFileName rule for
// the two path separators -- BadCharacters "\\" and "/" both become "+"
// (GoodCharacters), identically in Sonarr, Radarr and Lidarr (DeepWiki over
// their FileNameBuilder, 2026-10-01). The *arrs split a format into its
// folder components before substituting tokens, so a separator inside a
// token's value is never a separator; Render gets the same effect by
// replacing it in every token value. Only a template's own literal "/"
// separates components: "AC/DC" is one artist folder, "AC+DC", and a
// crowd-edited "x/../../../../etc" is the single name "x+..+..+..+..+etc"
// rather than a way out of the library. SanitizePath's own table (which
// maps both to "-") still applies to anything outside a token.
var pathSeparatorReplacer = strings.NewReplacer("/", "+", "\\", "+")

func replacePathSeparators(val string) string {
	return pathSeparatorReplacer.Replace(val)
}

// checkComponents refuses a rendered path with a component of only dots
// and spaces (ErrUnsafeComponent): "." and ".." are the folder itself and
// its parent, and SanitizePath would trim any other run of dots to
// nothing. A literal ".." in a template override is refused the same way.
func checkComponents(out string) error {
	for _, comp := range strings.Split(out, "/") {
		if strings.Contains(comp, ".") && strings.Trim(comp, ". ") == "" {
			return fmt.Errorf("%w: %q", ErrUnsafeComponent, comp)
		}
	}
	return nil
}

// Wrapper characters. docs/research/naming.md §A2 documents the rule -- "a
// token wrapped in extra characters is emitted only when non-empty" -- but
// not the character set; this one is Radarr's and Lidarr's FileNameBuilder
// TitleRegex (prefix [- ._[(]*, suffix [- ._)\]]*), as DeepWiki reads their
// source, unverified by a research note. A token may carry a run of them on
// either side of its name, and the whole run is emitted only when the token
// renders non-empty. That is how an optional token takes its separator or brackets
// with it: "{ (Release Year)}", "{ - Episode CleanTitle:90}",
// "{Release Year - }", "{-Release Group}".
const (
	wrapperPrefixChars = "- ._[("
	wrapperSuffixChars = "- ._)]"
)

func splitWrapper(spec string) (prefix, name, suffix string) {
	name = spec
	i := 0
	for i < len(name) && strings.IndexByte(wrapperPrefixChars, name[i]) >= 0 {
		i++
	}
	prefix, name = name[:i], name[i:]
	j := len(name)
	for j > 0 && strings.IndexByte(wrapperSuffixChars, name[j-1]) >= 0 {
		j--
	}
	suffix, name = name[j:], name[:j]
	return prefix, name, suffix
}

// dropEmptySegments removes every empty (or blank) '/'-segment from a
// rendered path, keeping a leading '/' if there was one. A multi-segment
// template whose middle segment is an optional token --
// "{Author Name}/{Book Series}/..." with no series -- otherwise renders
// "Author//...", which is no folder at all.
func dropEmptySegments(s string) string {
	if !strings.Contains(s, "/") {
		return s
	}
	parts := strings.Split(s, "/")
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, p)
		}
	}
	out := strings.Join(kept, "/")
	if strings.HasPrefix(s, "/") {
		out = "/" + out
	}
	return out
}

// splitModifier splits a token's ":" modifier off its name: zeros pad
// ("{season:00}"), a count truncates ("{Episode CleanTitle:90}"), and
// anything else is a filter, which only a language token takes
// ("{MediaInfo AudioLanguages:-EN}", Sonarr's customFormat group).
func splitModifier(name string) (base string, pad, trunc int, filter string) {
	i := lastColon(name)
	if i < 0 {
		return name, 0, 0, ""
	}
	mod := name[i+1:]
	if mod != "" && allZero(mod) {
		return name[:i], len(mod), 0, ""
	}
	if n, err := strconv.Atoi(mod); err == nil && n > 0 {
		return name[:i], 0, n, ""
	}
	if mod == "" {
		return name, 0, 0, ""
	}
	return name[:i], 0, 0, mod
}

func lastColon(s string) int {
	return strings.LastIndex(s, ":")
}

func allZero(s string) bool {
	for _, r := range s {
		if r != '0' {
			return false
		}
	}
	return true
}

func yearString(y int) string {
	if y == 0 {
		return ""
	}
	return strconv.Itoa(y)
}

func normalizeTokenName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// tokenEntry is one registered token: the function that renders it, and
// whether its output should be passed through the engine's configured
// ColonReplacement mode. Every *arr title-shaped token applies colon
// replacement, not just CleanTitle -- see the note's "Illegal characters
// and CleanTitle" section, which discusses colon replacement and
// CleanTitle together as the same normalisation pass.
type tokenEntry struct {
	fn             func(c Context, pad, trunc int) string
	colonSensitive bool
}

// tokenFuncs is grown by every later step; this step seeds it with the
// tokens exercised so far.
var tokenFuncs = map[string]tokenEntry{
	"movie title":             {fn: func(c Context, _, _ int) string { return c.Title }, colonSensitive: true},
	"movie cleantitle":        {fn: func(c Context, _, _ int) string { return cleanTitle(c.Title) }, colonSensitive: true},
	"movie titlethe":          {fn: func(c Context, _, _ int) string { return titleThe(c.Title) }, colonSensitive: true},
	"release year":            {fn: func(c Context, _, _ int) string { return yearString(c.Year) }},
	"release group":           {fn: func(c Context, _, _ int) string { return c.ReleaseGroup }},
	"tmdbid":                  {fn: func(c Context, _, _ int) string { return c.TmdbID }},
	"mediainfo audiocodec":    {fn: func(c Context, _, _ int) string { return firstAudioCodec(c.MediaInfo) }},
	"mediainfo audiochannels": {fn: func(c Context, _, _ int) string { return firstAudioChannels(c.MediaInfo) }},
	"mediainfo videocodec": {fn: func(c Context, _, _ int) string {
		return VideoCodecLabel(c.MediaInfo.VideoCodec, c.MediaInfo.VideoProfile, c.ReleaseTitle)
	}},
	"mediainfo videobitdepth": {fn: func(c Context, _, _ int) string { return nonZero(c.MediaInfo.VideoBitDepth) }},
	"mediainfo simple": {fn: func(c Context, _, _ int) string {
		return joinNonEmpty(" ",
			VideoCodecLabel(c.MediaInfo.VideoCodec, c.MediaInfo.VideoProfile, c.ReleaseTitle),
			firstAudioCodec(c.MediaInfo))
	}},
	"mediainfo full": {fn: func(c Context, _, _ int) string {
		return joinNonEmpty(" ",
			VideoCodecLabel(c.MediaInfo.VideoCodec, c.MediaInfo.VideoProfile, c.ReleaseTitle),
			firstAudioCodec(c.MediaInfo),
			languagesToken(audioLanguages(c.MediaInfo), "", configuredCode(c)),
			languagesToken(subtitleLanguages(c.MediaInfo), "", ""))
	}},
	"season":                          {fn: func(c Context, pad, _ int) string { return padInt(c.Season, pad) }},
	"episode":                         {fn: episodeToken},
	"absolute":                        {fn: func(c Context, pad, _ int) string { return padInt(firstOr(c.Absolute), pad) }},
	"episode cleantitle":              {fn: func(c Context, _, trunc int) string { return truncate(cleanTitle(c.EpisodeTitle), trunc) }},
	"quality full":                    {fn: func(c Context, _, _ int) string { return qualityFull(c.Quality, c.Revision, c.Anime) }},
	"mediainfo videodynamicrangetype": {fn: videoDynamicRangeType},
	"edition tags":                    {fn: func(c Context, _, _ int) string { return c.Edition }},
	"custom formats":                  {fn: func(c Context, _, _ int) string { return strings.Join(c.CustomFormats, " ") }},
	"series cleantitlewithoutyear":    {fn: func(c Context, _, _ int) string { return cleanTitle(withoutYear(c.SeriesTitle)) }, colonSensitive: true},
	// Sonarr's {Series Title} and {Series TitleWithoutYear}: the title as the
	// metadata provider spells it, apostrophes included ("Bob's Burgers"). The
	// series folder and episode file presets use TitleWithoutYear, so a new
	// show keeps its apostrophes on disk -- a deliberate departure, at the
	// project owner's request (2026-09-23), from TRaSH's recommended
	// {Series CleanTitleWithoutYear} (docs/research/naming.md), which strips
	// them. CleanTitle stays available for anyone who wants that. The
	// WithoutYear tokens strip a trailing "(YYYY)", as Sonarr's do: TVDB
	// titles a show sharing its name with another "Bluey (2018)", and the
	// templates append {(Series Year)} themselves (2026-09-30).
	"series title":            {fn: func(c Context, _, _ int) string { return c.SeriesTitle }, colonSensitive: true},
	"series titlewithoutyear": {fn: func(c Context, _, _ int) string { return withoutYear(c.SeriesTitle) }, colonSensitive: true},
	"series year":             {fn: func(c Context, _, _ int) string { return yearString(c.SeriesYear) }},
	"tvdbid":                  {fn: func(c Context, _, _ int) string { return c.TvdbID }},
	"air-date": {fn: func(c Context, _, _ int) string {
		if c.AirDate == nil {
			return ""
		}
		return c.AirDate.Format("2006-01-02")
	}},
	"artist name":         {fn: func(c Context, _, _ int) string { return c.ArtistName }},
	"album title":         {fn: func(c Context, _, _ int) string { return c.AlbumTitle }},
	"track title":         {fn: func(c Context, _, _ int) string { return c.TrackTitle }},
	"track":               {fn: func(c Context, pad, _ int) string { return padInt(c.Track, pad) }},
	"author name":         {fn: func(c Context, _, _ int) string { return c.AuthorName }},
	"book title":          {fn: func(c Context, _, _ int) string { return c.BookTitle }},
	"book series":         {fn: func(c Context, _, _ int) string { return c.BookSeries }},
	"book seriesposition": {fn: func(c Context, _, _ int) string { return c.BookSeriesPosition }},
	"narrator":            {fn: func(c Context, _, _ int) string { return c.Narrator }},
	// "issue" reads the pre-formatted string field directly, not through
	// padInt: Kavita issue numbers are strings like "001" or the decimal
	// chapter form "025.5", already formatted upstream.
	"comic series title": {fn: func(c Context, _, _ int) string { return c.ComicSeriesTitle }},
	"issue":              {fn: func(c Context, _, _ int) string { return c.IssueNumber }},
}

// videoDynamicRangeType renders {MediaInfo VideoDynamicRangeType}. Radarr's
// probe calls no stream below 10 bits HDR (VideoFileInfoReader.GetHdrFormat),
// so an 8-bit one renders none; a depth the probe did not measure (0) is
// not a judgement and keeps the range.
func videoDynamicRangeType(c Context, _, _ int) string {
	if d := c.MediaInfo.VideoBitDepth; d > 0 && d < 10 {
		return ""
	}
	return c.MediaInfo.Hdr.DisplayName()
}

// overrideOr looks up key in the engine's Config.Overrides, returning
// fallback when the key is absent or maps to an empty string.
func (e Engine) overrideOr(key, fallback string) string {
	if v, ok := e.Config.Overrides[key]; ok && v != "" {
		return v
	}
	return fallback
}

func padInt(n, width int) string {
	if width > 0 {
		return fmt.Sprintf("%0*d", width, n)
	}
	return strconv.Itoa(n)
}

// episodeToken renders an {episode} token that is not part of a
// season-episode pattern (expandMultiEpisode has already expanded those):
// the first and last episode joined by "-" when there are several, as
// Sonarr's own {Episode} token handler does
// (AddSeasonEpisodeNumberingTokens).
func episodeToken(c Context, pad, _ int) string {
	if len(c.Episodes) > 1 {
		return padInt(c.Episodes[0], pad) + "-" + padInt(c.Episodes[len(c.Episodes)-1], pad)
	}
	return padInt(firstOr(c.Episodes), pad)
}

func firstOr(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	return xs[0]
}

// truncate is rune-safe: it never splits a multi-byte rune.
func truncate(s string, n int) string {
	runes := []rune(s)
	if n <= 0 || len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// defaultAudioStream returns the audio stream the container itself flags
// as default, falling back to the first stream when none is flagged (or
// there is only one); ok is false when the probe found no audio at all.
// {MediaInfo AudioCodec}/{MediaInfo AudioChannels} read the default track,
// not simply the first one in probe order, because ffprobe does not
// guarantee stream order follows the container's own default flag.
func defaultAudioStream(mi commonv1.MediaInfo) (stream commonv1.AudioStream, ok bool) {
	if len(mi.Audio) == 0 {
		return commonv1.AudioStream{}, false
	}
	for _, a := range mi.Audio {
		if a.Default {
			return a, true
		}
	}
	return mi.Audio[0], true
}

func firstAudioCodec(mi commonv1.MediaInfo) string {
	a, ok := defaultAudioStream(mi)
	if !ok {
		return ""
	}
	return AudioCodecLabel(a.Codec, a.Profile)
}

// audioChannelLayout maps a raw channel count to the *arr channel-layout
// label. 6 physical channels is the well-known "5.1" layout (5 full-range +
// 1 low-frequency effects channel), not a bare "6.0".
var audioChannelLayout = map[int32]string{1: "1.0", 2: "2.0", 6: "5.1", 8: "7.1"}

func firstAudioChannels(mi commonv1.MediaInfo) string {
	a, ok := defaultAudioStream(mi)
	if !ok {
		return ""
	}
	if label, ok := audioChannelLayout[a.Channels]; ok {
		return label
	}
	return fmt.Sprintf("%d.0", a.Channels)
}

// nonZero renders an int32 count as a string, or "" for the probe's zero
// value (not yet measured), so an un-probed field's naming token collapses
// like any other empty token instead of rendering a literal "0".
func nonZero(n int32) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(int(n))
}

// audioLanguages and subtitleLanguages return each stream's language tag
// as the probe recorded it (ffprobe's ISO 639-2, "jpn"), in probe order;
// languagesToken does the rest.
func audioLanguages(mi commonv1.MediaInfo) []string {
	langs := make([]string, 0, len(mi.Audio))
	for _, a := range mi.Audio {
		langs = append(langs, a.Language)
	}
	return langs
}

func subtitleLanguages(mi commonv1.MediaInfo) []string {
	langs := make([]string, 0, len(mi.Subtitles))
	for _, s := range mi.Subtitles {
		langs = append(langs, s.Language)
	}
	return langs
}

// languageTokens are Sonarr's four language tokens (FileNameBuilder's
// AddMediaInfoTokens). Each takes a filter after a colon. {MediaInfo
// AudioLanguages} alone leaves a file whose only language is the item's
// configured one unstated; the subtitle tokens never do, as Sonarr's
// never skip English.
var languageTokens = map[string]func(c Context, filter string) string{
	"mediainfo audiolanguages": func(c Context, f string) string {
		return languagesToken(audioLanguages(c.MediaInfo), f, configuredCode(c))
	},
	"mediainfo audiolanguagesall":    func(c Context, f string) string { return languagesToken(audioLanguages(c.MediaInfo), f, "") },
	"mediainfo subtitlelanguages":    func(c Context, f string) string { return languagesToken(subtitleLanguages(c.MediaInfo), f, "") },
	"mediainfo subtitlelanguagesall": func(c Context, f string) string { return languagesToken(subtitleLanguages(c.MediaInfo), f, "") },
}

// languagesToken is Sonarr's GetLanguagesToken. Empty and "und" tags are
// dropped. Each tag becomes its upper-case two-letter code ("jpn" and "ja"
// are "JA", "pt-BR" is "PT"), or itself upper-cased when it names no
// language. Duplicates are dropped, first occurrence kept. filter "-EN"
// drops the codes it names; "JA+EN" keeps only those, in its own order;
// a trailing "+" appends "--" when it dropped any. Exactly one remaining
// code equal to skipOnly renders nothing (Sonarr's skipEnglishOnly, with
// the item's configured language in place of English). Anything else is
// "[A+B]".
func languagesToken(tags []string, filter, skipOnly string) string {
	codes := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || strings.EqualFold(t, "und") {
			continue
		}
		if code := languageCode(t); !slices.Contains(codes, code) {
			codes = append(codes, code)
		}
	}
	kept := codes
	filter = strings.ToUpper(filter)
	switch {
	case filter == "":
	case strings.HasPrefix(filter, "-"):
		drop := strings.Split(filter, "-")
		kept = slices.DeleteFunc(slices.Clone(codes), func(c string) bool { return slices.Contains(drop, c) })
	default:
		kept = nil
		for _, want := range strings.Split(filter, "+") {
			if want != "" && slices.Contains(codes, want) && !slices.Contains(kept, want) {
				kept = append(kept, want)
			}
		}
	}
	if strings.HasSuffix(filter, "+") && len(kept) != len(codes) {
		kept = append(kept, "--")
	}
	if len(kept) == 0 || len(kept) == 1 && kept[0] == skipOnly {
		return ""
	}
	return "[" + strings.Join(kept, "+") + "]"
}

// languageCode is tag's upper-case base language: ISO 639-1 where one
// exists ("jpn" is "JA", "fre" and "fra" are "FR", "pt-BR" is "PT"), as
// .NET's TwoLetterISOLanguageName gives Sonarr, else tag upper-cased.
func languageCode(tag string) string {
	if t, ok := lang.Normalize(tag); ok {
		tag, _, _ = strings.Cut(string(t), "-")
	}
	return strings.ToUpper(tag)
}

// configuredCode is the language code {MediaInfo AudioLanguages} leaves
// unstated when it is a file's only one: the item's configured language,
// else English, Sonarr's.
func configuredCode(c Context) string {
	if c.Language == "" {
		return "EN"
	}
	return languageCode(c.Language)
}

// joinNonEmpty joins the non-empty parts with sep, skipping any empty one
// so {MediaInfo Simple}/{MediaInfo Full} do not leave a dangling separator
// when, say, a probe found no audio track.
func joinNonEmpty(sep string, parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// trailingYear is a provider's disambiguating year at the end of a title:
// "Bluey (2018)".
var trailingYear = regexp.MustCompile(`\s*\(\d{4}\)$`)

// withoutYear is title without a trailing "(YYYY)".
func withoutYear(title string) string {
	return trailingYear.ReplaceAllString(title, "")
}
