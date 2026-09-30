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

package plex

import (
	"path"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

// Rule 0 of docs/superpowers/specs/2026-09-30-plex-provider-filename-match-design.md:
// PMS names the file it is matching, relative to its own library folder,
// and clustarr records which item every file backs (MediaFile spec.path,
// spec.mediaRef). A file that resolves to exactly one item is the answer;
// anything else is no answer, and the guid and title rules run as before.
// Never a guess between two items.

// cleanRelative vets a match request's filename: clean, relative, and not
// climbing out of the folder it is relative to.
func cleanRelative(filename string) (string, bool) {
	if filename == "" {
		return "", false
	}
	rel := path.Clean(filename)
	if rel == "." || rel == ".." || path.IsAbs(rel) || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

// filesFor returns the MediaFiles of kind whose path ends with filename.
func filesFor(idx *projection.Index, filename string, kind commonv1.MediaKind) []*catalogv1.MediaFile {
	rel, ok := cleanRelative(filename)
	if !ok {
		return nil
	}
	var out []*catalogv1.MediaFile
	for _, f := range idx.FilesEndingWith(rel) {
		if f.Spec.MediaRef.Kind == kind {
			out = append(out, f)
		}
	}
	return out
}

// movieByFile is rule 0 for type 1.
func movieByFile(idx *projection.Index, filename string) (*catalogv1.Movie, bool) {
	var found *catalogv1.Movie
	for _, f := range filesFor(idx, filename, commonv1.MediaKindMovie) {
		m, ok := idx.MovieByName(f.Namespace, f.Spec.MediaRef.Name)
		if !ok {
			continue
		}
		if found != nil && found.UID != m.UID {
			return nil, false
		}
		found = m
	}
	return found, found != nil
}

// episodesByFile returns every distinct Episode the file backs: the one
// spec.mediaRef names and, for a multi-episode file, those its keys name.
func episodesByFile(idx *projection.Index, filename string) []*catalogv1.Episode {
	seen := map[types.UID]bool{}
	var out []*catalogv1.Episode
	for _, f := range filesFor(idx, filename, commonv1.MediaKindEpisode) {
		names := append([]string{f.Spec.MediaRef.Name}, f.Spec.MediaRef.Keys...)
		for _, n := range names {
			e, ok := idx.EpisodeByName(f.Namespace, n)
			if !ok || seen[e.UID] {
				continue
			}
			seen[e.UID] = true
			out = append(out, e)
		}
	}
	return out
}

// showByFile is rule 0 for types 2 and 3: the one Series every Episode of
// the file belongs to.
func showByFile(idx *projection.Index, filename string) (*catalogv1.Series, bool) {
	var found *catalogv1.Series
	for _, e := range episodesByFile(idx, filename) {
		s, ok := idx.SeriesOfEpisode(e.UID)
		if !ok {
			continue
		}
		if found != nil && found.UID != s.UID {
			return nil, false
		}
		found = s
	}
	return found, found != nil
}

// episodeByFile is rule 0 for type 4: the file's Episode numbered as the
// request asks, else the file's only Episode.
func episodeByFile(idx *projection.Index, req matchRequest) (*catalogv1.Series, *catalogv1.Episode, bool) {
	s, ok := showByFile(idx, req.Filename)
	if !ok {
		return nil, nil, false
	}
	eps := episodesByFile(idx, req.Filename)
	if req.Index != nil && req.ParentIndex != nil {
		for _, e := range eps {
			if e.Spec.SeasonNumber == *req.ParentIndex && e.Spec.EpisodeNumber == *req.Index {
				return s, e, true
			}
		}
	}
	if len(eps) == 1 {
		return s, eps[0], true
	}
	return nil, nil, false
}

// leadWith puts first at the head of rest, dropping it from rest: a manual
// match (Plex's "Fix Match") lists the file's item first and the title
// search's other candidates after it.
func leadWith[T interface{ GetUID() types.UID }](first T, rest []T) []T {
	out := []T{first}
	for _, r := range rest {
		if r.GetUID() != first.GetUID() {
			out = append(out, r)
		}
	}
	return out
}
