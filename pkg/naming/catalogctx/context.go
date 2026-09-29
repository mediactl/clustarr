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
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
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

// formatNames is what {Custom Formats} names: the frozen matched formats,
// plus those named in a file that the release title matches now (Radarr
// works them out again when it names a file), by display name, the anime
// guide's only for an anime item.
func formatNames(ctx context.Context, c naming.Context, frozen []string) []string {
	cat := catalogue.LoadedCatalogue()
	slugs := frozen
	if c.ReleaseTitle != "" {
		if r, err := release.Parse(c.ReleaseTitle, release.Options{Kind: c.Kind}); err == nil {
			for _, s := range cat.RenameMatches(ctx, r, catalogue.ItemContext{ReleaseTitle: c.ReleaseTitle}) {
				if !slices.Contains(slugs, s) {
					slugs = append(slices.Clip(slugs), s)
				}
			}
		}
	}
	return cat.NamesForRename(slugs, c.Anime)
}

// ContainerExt maps mi's probed container to a file extension. It reads
// both vocabularies a container arrives in: what pkg/mediainfo.Probe
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

// MovieFilePath renders m's absolute library path under root:
// root.spec.path, joined with m.spec.folder when set and non-empty, else
// the dialect's movie-folder preset, joined with the dialect's movie-file
// preset and ext -- then run through naming.SanitizePath, the one
// general-purpose sanitiser pkg/naming exports, which no per-kind renderer
// calls on its own (see pkg/naming's doc comment). The caller is the first
// thing to actually write this path to a filesystem, so it is the one that
// must make it filesystem-safe.
func MovieFilePath(root *catalogv1alpha1.RootFolder, m *catalogv1alpha1.Movie, c naming.Context, ext string) (string, error) {
	eng := EngineFor(root)
	folder := ptr.Deref(m.Spec.Folder, "")
	if folder == "" {
		rendered, err := eng.MovieFolder(c)
		if err != nil {
			return "", fmt.Errorf("catalogctx: render movie folder: %w", err)
		}
		folder = rendered
	}
	file, err := eng.MovieFile(c)
	if err != nil {
		return "", fmt.Errorf("catalogctx: render movie file: %w", err)
	}
	full := filepath.Join(root.Spec.Path, folder, file+ext)
	return naming.SanitizePath(full, naming.DefaultSanitizeOptions()), nil
}

// EpisodeFilePath renders c's absolute library path under root: s' own
// folder (status.path once the Series controller has resolved one,
// matching app/catalog/controller/series.Path's rule -- else spec.folder
// under root.spec.path, else the dialect's series preset), the season
// folder when the series keeps one (spec.seasonFolder, defaulted true),
// and the dialect's episode/anime/daily file preset (selected by c's own
// fields, as Engine.EpisodeFile already does) with ext, sanitized exactly
// as MovieFilePath's result is.
func EpisodeFilePath(root *catalogv1alpha1.RootFolder, s *catalogv1alpha1.Series, c naming.Context, ext string) (string, error) {
	eng := EngineFor(root)
	folder, err := seriesFolder(root, s, eng, c)
	if err != nil {
		return "", fmt.Errorf("catalogctx: render series folder: %w", err)
	}
	if ptr.Deref(s.Spec.SeasonFolder, true) {
		season, err := eng.SeasonFolder(c)
		if err != nil {
			return "", fmt.Errorf("catalogctx: render season folder: %w", err)
		}
		folder = filepath.Join(folder, season)
	}
	file, err := eng.EpisodeFile(c)
	if err != nil {
		return "", fmt.Errorf("catalogctx: render episode file: %w", err)
	}
	full := filepath.Join(folder, file+ext)
	return naming.SanitizePath(full, naming.DefaultSanitizeOptions()), nil
}

// seriesFolder is s' absolute folder in the library: status.path once the
// Series controller has resolved it, else the same rule that controller
// resolves it by (app/catalog/controller/series.Path) -- spec.folder under
// root, else the dialect's series preset rendered from c.
func seriesFolder(root *catalogv1alpha1.RootFolder, s *catalogv1alpha1.Series, eng naming.Engine, c naming.Context) (string, error) {
	if s.Status.Path != "" {
		return s.Status.Path, nil
	}
	if f := ptr.Deref(s.Spec.Folder, ""); f != "" {
		return path.Join(root.Spec.Path, f), nil
	}
	folder, err := eng.SeriesFolder(c)
	if err != nil {
		return "", err
	}
	return path.Join(root.Spec.Path, folder), nil
}
