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

// Package catalogctx is the one place a naming.Context is built from
// catalog CRDs and rendered into a MediaFile's destination path -- for the
// import worker (app/import/worker/fileimport) today, and for the rename
// controllers docs/superpowers/plans/2026-09-24-probe-driven-naming.md adds
// later. Movie and Episode build the identity half of a Context (title,
// year, provider ids, season/episode numbering) from the catalog item
// alone; File merges in the release-time and probe-time half (quality,
// revision, group, custom formats, MediaInfo) once a MediaFileSpec and,
// once probed, a MediaInfo are known. MovieFilePath and EpisodeFilePath
// render the two together into an absolute library path, matching each
// item's own controller-resolved folder rule
// (app/catalog/controller/movie, app/catalog/controller/series.Path).
package catalogctx

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/release"
)

// EngineFor builds the naming engine a root folder's naming spec selects:
// dialect, colon replacement, multi-episode style and per-target template
// overrides, exactly as every renderer this package replaces built it.
func EngineFor(root *catalogv1alpha1.RootFolder) naming.Engine {
	return naming.NewEngine(naming.Config{
		Dialect:           naming.Dialect(root.Spec.Naming.Dialect),
		ColonReplacement:  naming.ColonReplacement(root.Spec.Naming.ColonReplacement),
		MultiEpisodeStyle: naming.MultiEpisodeStyle(root.Spec.Naming.MultiEpisodeStyle),
		Overrides:         root.Spec.Naming.Overrides,
	})
}

// ReasonFolderUnrenderable is the Ready reason every item controller
// reports when ItemFolder, BookFolder or AlbumFolder cannot render the
// item's folder from a title it knows (2026-11-10 design §5.5): one the
// engine refuses (naming.ErrUnsafeComponent, a title of only dots) or the
// sanitizer empties ("???"). The controller keeps the status it has
// (reassertKnownStatus) and reports the problem on Ready, never a path and
// never an error that would back the item off for good; the next metadata
// refresh, spec edit or RootFolder change renders it again.
const ReasonFolderUnrenderable = "FolderUnrenderable"

// ItemFolder is the folder an item of kind lives in under root, as the
// import places its files and as the item's controller writes status.path
// (app/catalog/controller/<kind>.Path), so the two cannot diverge
// (2026-11-10 design §5.5). It is the rule of the kinds with a folder of
// their own under the root -- a Movie, a Series, an Artist, an Author, an
// Audiobook and a Comic; any other kind is naming.ErrNoFolder. A Book's
// folder is BookFolder's, an Album's AlbumFolder's, and a file's path is
// its kind's *FilePath, which joins the file onto this folder.
//
// A pinned folder -- the item's spec.folder, when it is set and not blank
// -- is taken as written: root.spec.path joined with its components, none
// sanitized or shortened (pinnedFolder). It names a folder on disk, not a
// title: the scan applier pins a scanned Series to the folder whose
// TheTVDB id named it (app/import/manager/scanapply), and a person who
// sets it names the folder they have, so "What If...? (2021)
// [tvdbid-266189]" is that folder, where the sanitized "What If... (2021)
// [tvdbid-266189]" would be one beside it that nothing created. Only a
// component that walks the tree (".", "..") or is blank is refused
// (naming.ErrUnsafeComponent), and the result must lie strictly under the
// root (errOutsideRoot); a blank spec.folder is unset.
//
// Without a pin the folder is the kind's preset rendered from c by the
// root's engine, every component sanitized and the result held strictly
// under the root exactly as the file path is (joinUnder), so a title with
// a "?", a trailing dot or 390 bytes names the folder the file lands in
// ("What If" for "What If...?"), not the title as written. A preset that
// renders nothing -- an Author or an Artist whose name has not arrived --
// is the root itself, the degraded answer while the title is unknown
// (folderUnder); a known title the engine refuses (one of only dots,
// naming.ErrUnsafeComponent) or the sanitizer empties ("???") is an error,
// which each controller reports as ReasonFolderUnrenderable on Ready.
func ItemFolder(root *catalogv1alpha1.RootFolder, kind commonv1.MediaKind, folder *string, c naming.Context) (string, error) {
	switch kind {
	case commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindArtist,
		commonv1.MediaKindAuthor, commonv1.MediaKindAudiobook, commonv1.MediaKindComic:
	default:
		return "", fmt.Errorf("catalogctx: %w: %s", naming.ErrNoFolder, kind)
	}
	if pin, ok := pinnedFolder(folder); ok {
		return literalFolder(root.Spec.Path, pin)
	}
	rendered, err := EngineFor(root).BuildFolder(kind, c)
	if err != nil {
		return "", fmt.Errorf("catalogctx: render %s folder: %w", kind, err)
	}
	return folderUnder(root.Spec.Path, root.Spec.Path, rendered)
}

// pinnedFolder is an item's spec.folder as a pin: its value with the
// surrounding whitespace removed, and whether it pins anything at all. A
// nil, empty or blank spec.folder pins nothing, and the item's folder is
// its preset's.
func pinnedFolder(folder *string) (string, bool) {
	pin := strings.TrimSpace(ptr.Deref(folder, ""))
	return pin, pin != ""
}

// literalFolder is a pinned folder under root, as written: every
// '/'-component kept as it is, a blank one or one that walks the tree
// (".", "..") refused as naming.ErrUnsafeComponent, and the join held
// strictly under root (errOutsideRoot). No component is sanitized or
// shortened: the pin names a folder on disk, which the sanitizer's rewrite
// of a "?" or a trailing dot would turn into another one.
func literalFolder(root, pin string) (string, error) {
	parts := []string{root}
	for _, comp := range strings.Split(filepath.ToSlash(pin), "/") {
		if comp == "" {
			continue
		}
		if strings.TrimSpace(comp) == "" || comp == "." || comp == ".." {
			return "", fmt.Errorf("catalogctx: %w: %q in spec.folder %q", naming.ErrUnsafeComponent, comp, pin)
		}
		parts = append(parts, comp)
	}
	full := filepath.Join(parts...)
	if !fsops.StrictlyUnder(full, root) {
		return "", fmt.Errorf("%w: spec.folder %q under %q", errOutsideRoot, pin, root)
	}
	return full, nil
}

// resolvedFolder is a container's folder as its files are placed under it,
// so a file imported before the controller's first pass lands where the
// controller will say it lives, and one imported after it lands where the
// controller said: a pinned item's folder is its pin (ItemFolder, taken as
// written, whatever status.path says -- the two are the same join); an
// unpinned one's is its resolved status.path when it has one, the
// controller's answer as of its last pass, sanitized below the root
// (renderedBase) so a status.path a controller wrote by the rule before
// 2026-11-10 -- the title as written, "What If...?" -- still names the
// sanitized folder the files were placed in, as it did when the whole path
// was sanitized at the join; else ItemFolder from the same inputs.
func resolvedFolder(root *catalogv1alpha1.RootFolder, statusPath string, kind commonv1.MediaKind, folder *string, c naming.Context) (string, error) {
	if _, pinned := pinnedFolder(folder); pinned {
		return ItemFolder(root, kind, folder, c)
	}
	if statusPath != "" {
		return renderedBase(root.Spec.Path, statusPath), nil
	}
	return ItemFolder(root, kind, folder, c)
}

// renderedBase is a rendered folder -- a container's status.path, the
// controller's render of its preset -- as a base for a join: its path below
// root sanitized to the sanitizer's fixed point, the root itself as it is.
// A status.path the new rule wrote is a fixed point already and comes back
// unchanged; one the old rule wrote (the title as written, path.Join)
// becomes the folder on disk, where the files were placed under the
// whole-path sanitize every join ran until then. A status.path not under
// the root is returned cleaned, for joinUnder to refuse.
func renderedBase(root, statusPath string) string {
	rel, err := filepath.Rel(root, statusPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Clean(statusPath)
	}
	return filepath.Join(root, sanitizeFixed(rel, naming.DefaultSanitizeOptions()))
}

// Movie builds the naming.Context describing m's identity: title, year and
// provider ids -- everything the movie folder and file presets' non-release
// tokens need. Title and OriginalTitle both read status.metadata.title;
// Clustarr does not yet carry a separate original-language title. ok is
// false until m's metadata has arrived (status.metadata.title set), the
// same gate every caller of this package already held before it could
// render a path.
func Movie(m *catalogv1alpha1.Movie) (naming.Context, bool) {
	if m.Status.Metadata == nil || m.Status.Metadata.Title == "" {
		return naming.Context{}, false
	}
	md := m.Status.Metadata
	return naming.Context{
		Kind:          commonv1.MediaKindMovie,
		Title:         md.Title,
		OriginalTitle: md.Title,
		Year:          int(md.Year),
		TmdbID:        strconv.FormatInt(m.Spec.TmdbID, 10),
		ImdbID:        md.ExternalIDs["imdb"],
	}, true
}

// Episode builds the naming.Context describing eps -- one or more episodes
// of series s that a single MediaFile backs, sorted and non-empty: series
// identity from s, Season/EpisodeTitle/Special from eps[0], every episode's
// number in Episodes, and Absolute filled only when every one of eps
// carries a nonzero absolute number (a multi-episode anime file with one
// absolute-less episode renders no absolute segment at all, matching
// Sonarr), and AirDate parsed from eps[0]'s air date for a daily series.
// ok is false until s's metadata has arrived or eps is empty.
func Episode(s *catalogv1alpha1.Series, eps []catalogv1alpha1.Episode) (naming.Context, bool) {
	if s.Status.Metadata == nil || s.Status.Metadata.Title == "" || len(eps) == 0 {
		return naming.Context{}, false
	}
	md := s.Status.Metadata
	first := eps[0]
	c := naming.Context{
		Kind:         commonv1.MediaKindEpisode,
		SeriesTitle:  md.Title,
		SeriesYear:   int(md.Year),
		TvdbID:       strconv.FormatInt(s.Spec.TvdbID, 10),
		Season:       int(first.Spec.SeasonNumber),
		EpisodeTitle: first.Status.Title,
		Special:      first.Spec.SeasonNumber == 0,
	}
	c.Anime = s.Spec.SeriesType == catalogv1alpha1.SeriesTypeAnime
	absolute := c.Anime
	for _, e := range eps {
		c.Episodes = append(c.Episodes, int(e.Spec.EpisodeNumber))
		if e.Status.AbsoluteNumber == nil || *e.Status.AbsoluteNumber == 0 {
			absolute = false
		}
	}
	if absolute {
		for _, e := range eps {
			c.Absolute = append(c.Absolute, int(*e.Status.AbsoluteNumber))
		}
	}
	if s.Spec.SeriesType == catalogv1alpha1.SeriesTypeDaily && first.Status.AirDate != nil && !first.Status.AirDate.IsZero() {
		t := first.Status.AirDate.UTC()
		c.AirDate = &t
	}
	return c, true
}

// File merges a MediaFile's release-time fields and, once probed, its
// technical description into c, returning the result: everything the
// shared "{ - [Quality Full]}{ [MediaInfo VideoDynamicRangeType]}{
// [MediaInfo VideoCodec]}{-Release Group}" segment of every video preset
// needs, beyond the item identity Movie or Episode already set: the
// matched formats by the names {Custom Formats} shows
// (catalogue.NamesForRename), and a release title, falling back to the
// file's own name. spec is
// nil-safe -- a caller with no MediaFileSpec yet (there is none before the
// first import) gets c back unchanged by this half of the merge. mi nil
// (not yet probed) leaves c.MediaInfo exactly as it was, so a video
// preset's MediaInfo tokens render empty until a probe result exists.
func File(ctx context.Context, c naming.Context, spec *catalogv1alpha1.MediaFileSpec, mi *commonv1.MediaInfo) naming.Context {
	if spec != nil {
		c.Quality = spec.Quality
		c.Revision = spec.Revision
		c.ReleaseGroup = spec.ReleaseGroup
		c.Edition = spec.Edition
		if spec.ImportedFrom != nil {
			c.ReleaseTitle = spec.ImportedFrom.ReleaseTitle
		}
		if c.ReleaseTitle == "" && spec.Path != "" {
			// Radarr's GetSceneOrFileName: with no release title (a file a
			// rescan found), the file's own name says whether it is an
			// x264 or an h264 encode, and which service it came from.
			c.ReleaseTitle = strings.TrimSuffix(filepath.Base(spec.Path), filepath.Ext(spec.Path))
		}
		c.CustomFormats = formatNames(ctx, c, spec.MatchedFormats)
	}
	if mi != nil {
		c.MediaInfo = *mi
	}
	return c
}

// ConfiguredLanguage is naming.Context.Language for an item ranked against
// p. A file whose only audio language this is reads no {MediaInfo
// AudioLanguages} block, so any other lone language -- the original's, or
// neither -- is always named. It is the first language p names by tag: its
// audio default, then its audio languages in order (the anime profiles' en
// in [en, original]), or its language when it has no audio policy.
// "original" names none, so a French film with French audio alone under an
// original-language profile reads [FR], as in Sonarr. Without a tag, or
// without a profile, it is English, Sonarr's.
func ConfiguredLanguage(p *quality.Profile) string {
	if p == nil {
		return "en"
	}
	names := []string{p.Language}
	if len(p.AudioLanguages) > 0 {
		names = append([]string{p.AudioDefault}, p.AudioLanguages...)
	}
	for _, n := range names {
		if n == "" || n == "original" || n == "any" {
			continue
		}
		if t, ok := lang.Normalize(n); ok {
			return string(t)
		}
	}
	return "en"
}

// formatNamesMemo remembers formatNames's answer by its inputs: the file
// pass renders every file's name on every pass, and parsing the release
// title and running the catalogue's regexes over it again was 7% of the
// manager's CPU (talos, 2026-11-03). The catalogue is loaded once, so an
// answer never goes stale; the memo is emptied once it holds
// formatNamesMemoMax answers.
var formatNamesMemo = struct {
	sync.Mutex
	m map[formatNamesKey][]string
}{m: map[formatNamesKey][]string{}}

const formatNamesMemoMax = 32768

type formatNamesKey struct {
	title  string
	kind   commonv1.MediaKind
	anime  bool
	frozen string
}

// formatNames is what {Custom Formats} names: the frozen matched formats,
// plus those named in a file that the release title matches now (Radarr
// works them out again when it names a file), by display name, the anime
// guide's only for an anime item.
func formatNames(ctx context.Context, c naming.Context, frozen []string) []string {
	cat := catalogue.LoadedCatalogue()
	if c.ReleaseTitle == "" {
		return cat.NamesForRename(frozen, c.Anime)
	}
	key := formatNamesKey{title: c.ReleaseTitle, kind: c.Kind, anime: c.Anime, frozen: strings.Join(frozen, "\x00")}
	formatNamesMemo.Lock()
	names, ok := formatNamesMemo.m[key]
	formatNamesMemo.Unlock()
	if ok {
		return slices.Clone(names)
	}
	slugs := frozen
	if r, err := release.Parse(c.ReleaseTitle, release.Options{Kind: c.Kind}); err == nil {
		for _, s := range cat.RenameMatches(ctx, r, catalogue.ItemContext{ReleaseTitle: c.ReleaseTitle}) {
			if !slices.Contains(slugs, s) {
				slugs = append(slices.Clip(slugs), s)
			}
		}
	}
	names = cat.NamesForRename(slugs, c.Anime)
	formatNamesMemo.Lock()
	if len(formatNamesMemo.m) >= formatNamesMemoMax {
		formatNamesMemo.m = map[formatNamesKey][]string{}
	}
	formatNamesMemo.m[key] = slices.Clone(names)
	formatNamesMemo.Unlock()
	return names
}

// ContainerExt maps mi's probed container to a file extension. It reads
// both vocabularies a container arrives in: what the probe (pkg/mediainfo/native)
// actually records in MediaInfo.Container -- the probed file's own
// extension, lowercased and without its dot (mediainfo's
// containerFromPath) -- and ffprobe's format_name, a comma-separated
// group ("matroska,webm" for both Matroska-family containers,
// "mov,mp4,m4a,3gp,3g2,mj2" for every ISO base media file format variant).
// mi nil (not yet probed) or an unrecognised container falls back to
// fallbackPath's own extension -- the source file's, before this file's
// first probe.
func ContainerExt(mi *commonv1.MediaInfo, fallbackPath string) string {
	if mi != nil {
		switch mi.Container {
		case "mkv", "webm", "matroska", "matroska,webm":
			return ".mkv"
		case "mp4", "mov,mp4,m4a,3gp,3g2,mj2":
			return ".mp4"
		case "avi":
			return ".avi"
		}
	}
	return filepath.Ext(fallbackPath)
}

// MovieFilePath renders m's absolute library path under root: the movie's
// folder (ItemFolder: m.spec.folder as written when it pins one, else the
// dialect's movie-folder preset) joined with the dialect's movie-file
// preset and ext. Every rendered component is made filesystem-safe by
// naming.SanitizePath -- the one general-purpose sanitiser pkg/naming
// exports, which no per-kind renderer calls on its own (see pkg/naming's
// doc comment) -- before anything is joined, and the result must lie
// strictly under root.spec.path (joinUnder). The caller is the first
// thing to actually write this path to a filesystem, so it is the one that
// must make it filesystem-safe.
func MovieFilePath(root *catalogv1alpha1.RootFolder, m *catalogv1alpha1.Movie, c naming.Context, ext string) (string, error) {
	folder, err := ItemFolder(root, commonv1.MediaKindMovie, m.Spec.Folder, c)
	if err != nil {
		return "", err
	}
	file, err := EngineFor(root).MovieFile(c)
	if err != nil {
		return "", fmt.Errorf("catalogctx: render movie file: %w", err)
	}
	return joinUnder(root.Spec.Path, folder, file+ext)
}

// EpisodeFilePath renders c's absolute library path under root: s' own
// folder (resolvedFolder: its spec.folder as written when it pins one, else
// status.path once the Series controller has resolved one, else
// ItemFolder's rule, the one app/catalog/controller/series.Path writes it
// by, the dialect's series preset), the season folder when the series keeps one
// (spec.seasonFolder, defaulted true), and the dialect's episode/anime/daily
// file preset (selected by c's own fields, as Engine.EpisodeFile already
// does) with ext, sanitized and held under root.spec.path exactly as
// MovieFilePath's result is -- a status.path outside the root folder is
// refused, not followed.
func EpisodeFilePath(root *catalogv1alpha1.RootFolder, s *catalogv1alpha1.Series, c naming.Context, ext string) (string, error) {
	eng := EngineFor(root)
	base, err := resolvedFolder(root, s.Status.Path, commonv1.MediaKindSeries, s.Spec.Folder, c)
	if err != nil {
		return "", err
	}
	var rels []string
	if ptr.Deref(s.Spec.SeasonFolder, true) {
		season, err := eng.SeasonFolder(c)
		if err != nil {
			return "", fmt.Errorf("catalogctx: render season folder: %w", err)
		}
		rels = append(rels, season)
	}
	file, err := eng.EpisodeFile(c)
	if err != nil {
		return "", fmt.Errorf("catalogctx: render episode file: %w", err)
	}
	return joinUnder(root.Spec.Path, base, append(rels, file+ext)...)
}

// errOutsideRoot is wrapped by every path builder here whose result would
// not lie strictly under its root folder's path.
var errOutsideRoot = errors.New("catalogctx: path is not under the root folder")

// joinUnder joins rels onto base, every '/'-component of every rel
// sanitized on its own (sanitizeFixed over componentOptions) before the
// join, so a join can never resolve a ".." a title carried: a component of
// only dots is refused (naming.ErrUnsafeComponent) rather than trimmed to
// nothing and silently dropped, and so is one the sanitiser empties. The
// rels are capped together at what PATH_MAX leaves under base (the last
// segment shrunk, as naming.SanitizePath caps a path). base is taken as
// given -- the caller's folder: the root, a pinned folder as written, or a
// rendered one sanitized by renderedBase or by this function before -- and
// the result must lie strictly under root (fsops.StrictlyUnder), the
// defence that holds even a template override or a status.path to the
// library. Until 2026-11-10 the whole join was sanitized once more, base
// included, which rewrote a pinned folder's "?" out of the path.
func joinUnder(root, base string, rels ...string) (string, error) {
	var comps []string
	for _, rel := range rels {
		for _, comp := range strings.Split(rel, "/") {
			if strings.TrimSpace(comp) == "" {
				continue
			}
			if strings.Trim(comp, ". ") == "" {
				return "", fmt.Errorf("catalogctx: %w: %q", naming.ErrUnsafeComponent, comp)
			}
			clean := sanitizeFixed(comp, componentOptions)
			if clean == "" {
				return "", fmt.Errorf("catalogctx: component %q sanitizes to nothing", comp)
			}
			comps = append(comps, clean)
		}
	}
	base = filepath.Clean(base)
	rel := strings.Join(comps, "/")
	if rel != "" {
		budget := naming.DefaultMaxTotalBytes - len(base) - 1
		if budget < 1 {
			return "", fmt.Errorf("%w: %q leaves no room under PATH_MAX", errOutsideRoot, base)
		}
		opts := naming.DefaultSanitizeOptions()
		opts.MaxTotalBytes = budget
		rel = sanitizeFixed(rel, opts)
	}
	full := filepath.Join(base, rel)
	if !fsops.StrictlyUnder(full, root) {
		return "", fmt.Errorf("%w: %q under %q", errOutsideRoot, full, root)
	}
	return full, nil
}

// sanitizeFixed is naming.SanitizePath run to its fixed point: a result
// that sanitizes to itself, so a path this package renders is the same
// path sanitized again, and a stale status.path, a fresh render and a
// written one all join alike (TestAStaleStatusPathRendersTheSameFilePath).
// One pass is not that: it trims a component's trailing dots and spaces
// before it cuts the component at 255 bytes, so a 390-byte title whose cut
// lands on a space ("Long Long ... Long ") comes out of one pass 255 bytes
// with a trailing space and out of a second 254, and the two name
// different folders (the F1 review, 2026-11-10). Every pass is
// non-increasing in length and replaces nothing a pass before it kept, so
// the loop ends within a few passes; the bound is a belt.
func sanitizeFixed(s string, opts naming.SanitizeOptions) string {
	for range 8 {
		next := naming.SanitizePath(s, opts)
		if next == s {
			return s
		}
		s = next
	}
	return s
}

// folderUnder is joinUnder for a folder rather than a file: rel's
// components are sanitized and joined onto base and held strictly under
// root exactly as a file's are, except that a rel of no components at all
// -- a preset that rendered nothing, the folder of a container whose name
// has not arrived -- is base itself, cleaned, where joinUnder would refuse
// the root as a file's path. That is the one degraded answer this package
// gives: an item without a title yet lives at its root until it has one.
func folderUnder(root, base, rel string) (string, error) {
	for _, comp := range strings.Split(rel, "/") {
		if strings.TrimSpace(comp) != "" {
			return joinUnder(root, base, rel)
		}
	}
	return filepath.Clean(base), nil
}

// componentOptions sanitizes one path component: '/' is illegal in it, not
// a separator (pkg/naming has already made a token's own "/" a "+").
var componentOptions = naming.SanitizeOptions{
	ReplaceIllegal:    true,
	MaxComponentBytes: naming.DefaultMaxComponentBytes,
	MaxTotalBytes:     naming.DefaultMaxComponentBytes,
}
