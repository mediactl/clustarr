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
	"maps"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// movieHashMinBytes is the smallest file OpenSubtitles' moviehash is defined
// for: the first and last 64 KiB (pkg/mediainfo.ErrTooSmall).
const movieHashMinBytes = 128 << 10

// buildQuery is the providers' query (loop spec §6.7.2 step 6): the task's
// item ids, title, year, season and episode, which the loop read from the
// item, plus the file's size, OpenSubtitles' moviehash when the file is
// large enough, and the release parsed from the task's release title.
func buildQuery(ctx context.Context, t schema.FetchTask, local string, size int64, key subtitles.LangKey) subtitles.Query {
	q := subtitles.Query{
		Kind:      t.Query.Kind,
		IDs:       maps.Clone(t.Query.IDs),
		Title:     t.Query.Title,
		Year:      int(t.Query.Year),
		Season:    int(t.Query.Season),
		Episode:   int(t.Query.Episode),
		SizeBytes: size,
		Languages: []subtitles.LangKey{key},
		Release:   parsedRelease(t.Query.ReleaseTitle, t.Query.Kind),
	}
	if q.IDs == nil {
		q.IDs = map[string]string{}
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
	return q
}

// parsedRelease parses the release the file came from, which GuessMatches
// compares each candidate's release_info against: the original release
// title when the import recorded one, else the file name (the loop sends
// whichever it has). An unparseable name yields nil, which scores
// candidates without release corroboration rather than failing.
func parsedRelease(title string, kind commonv1.MediaKind) *release.ParsedRelease {
	if title == "" {
		return nil
	}
	pr, err := release.Parse(title, release.Options{Kind: kind})
	if err != nil {
		return nil
	}
	return pr
}
