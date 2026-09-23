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
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// movieHashMinBytes is the smallest file OpenSubtitles' moviehash is defined
// for: the first and last 64 KiB (pkg/mediainfo.ErrTooSmall).
const movieHashMinBytes = 128 << 10

// buildQuery is spec §6.5's "load MediaFile + owner metadata (ids, title,
// year, season/episode) ... moviehash if >= 128 KiB". The owner is the
// Movie, or the Episode and its Series; a missing owner or missing metadata
// leaves those fields empty rather than failing -- a hash-only search is
// still a real search, and [searchable] decides per provider whether what
// is left can identify the item.
func (w *Worker) buildQuery(ctx context.Context, mf *catalogv1alpha1.MediaFile, local string, size int64, key subtitles.LangKey) (subtitles.Query, error) {
	kind := mf.Spec.MediaRef.Kind
	q := subtitles.Query{
		Kind:      kind,
		IDs:       map[string]string{},
		SizeBytes: size,
		Languages: []subtitles.LangKey{key},
		Release:   targetRelease(mf, kind),
	}

	switch kind {
	case commonv1.MediaKindMovie:
		var movie catalogv1alpha1.Movie
		err := w.Client.Get(ctx, types.NamespacedName{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}, &movie)
		switch {
		case apierrors.IsNotFound(err):
		case err != nil:
			return subtitles.Query{}, fmt.Errorf("get movie %s: %w", mf.Spec.MediaRef.Name, err)
		default:
			if movie.Spec.TmdbID > 0 {
				q.IDs["tmdb"] = strconv.FormatInt(movie.Spec.TmdbID, 10)
			}
			if md := movie.Status.Metadata; md != nil {
				q.Title, q.Year = md.Title, int(md.Year)
				setID(q.IDs, "imdb", imdbNumber(md.ExternalIDs["imdb"]))
			}
		}
	case commonv1.MediaKindEpisode:
		var ep catalogv1alpha1.Episode
		err := w.Client.Get(ctx, types.NamespacedName{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}, &ep)
		switch {
		case apierrors.IsNotFound(err):
		case err != nil:
			return subtitles.Query{}, fmt.Errorf("get episode %s: %w", mf.Spec.MediaRef.Name, err)
		default:
			q.Season, q.Episode = int(ep.Spec.SeasonNumber), int(ep.Spec.EpisodeNumber)
			var series catalogv1alpha1.Series
			err := w.Client.Get(ctx, types.NamespacedName{Namespace: mf.Namespace, Name: ep.Spec.SeriesRef}, &series)
			switch {
			case apierrors.IsNotFound(err):
			case err != nil:
				return subtitles.Query{}, fmt.Errorf("get series %s: %w", ep.Spec.SeriesRef, err)
			default:
				if series.Spec.TvdbID > 0 {
					q.IDs["tvdb"] = strconv.FormatInt(series.Spec.TvdbID, 10)
				}
				if md := series.Status.Metadata; md != nil {
					q.Title, q.Year = md.Title, int(md.Year)
					// pkg/subtitles.Query's documented keys for the show's
					// own ids; "imdb"/"tmdb" would claim they are the
					// episode's.
					setID(q.IDs, "parent_imdb", imdbNumber(md.ExternalIDs["imdb"]))
					setID(q.IDs, "parent_tmdb", md.ExternalIDs["tmdb"])
				}
			}
		}
	}

	if size >= movieHashMinBytes {
		h, err := mediainfo.MovieHash(local)
		switch {
		case err == nil:
			q.Hash = h
		case errors.Is(err, mediainfo.ErrTooSmall):
		default:
			// A hash is an optimisation, not a precondition: search without
			// one rather than fail the task on an unreadable tail.
			logging.FromContext(ctx).Warn("fetch: could not compute the movie hash; searching without it", "err", err)
		}
	}
	return q, nil
}

// targetRelease parses the release the file came from, which GuessMatches
// compares each candidate's release_info against: the original release
// title when the import recorded one, else the file name. An unparseable
// name yields nil, which scores candidates without release corroboration
// rather than failing.
func targetRelease(mf *catalogv1alpha1.MediaFile, kind commonv1.MediaKind) *release.ParsedRelease {
	title := ""
	if mf.Spec.ImportedFrom != nil {
		title = mf.Spec.ImportedFrom.ReleaseTitle
	}
	if title == "" {
		base := filepath.Base(mf.Spec.Path)
		title = strings.TrimSuffix(base, filepath.Ext(base))
	}
	pr, err := release.Parse(title, release.Options{Kind: kind})
	if err != nil {
		return nil
	}
	return pr
}

// imdbNumber strips an IMDb id to the bare number OpenSubtitles expects: no
// "tt" prefix, no leading zeros (pkg/subtitles.Query's IDs doc comment). A
// value that is not an IMDb id at all is dropped rather than passed through.
func imdbNumber(id string) string {
	n := strings.TrimLeft(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(id)), "tt"), "0")
	if n == "" {
		return ""
	}
	if _, err := strconv.ParseUint(n, 10, 64); err != nil {
		return ""
	}
	return n
}

func setID(ids map[string]string, key, val string) {
	if val != "" {
		ids[key] = val
	}
}
