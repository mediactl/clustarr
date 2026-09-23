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

package facade

import (
	"context"
	"net/http"

	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/newznab"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// releaseToTorznab renders one schema.Release -- the already-parsed,
// already-merged shape both rpc.indexarr.search and rpc.indexarr.query
// answer with -- as the torznab.Release WriteResults expects.
//
// It reads only commonv1.ReleaseInfo (rel.Info): the classifier fields
// alongside it (ParsedTitle, Seasons, Kind, Hints, ...) describe how
// Clustarr's own decision engine read the release, which has no Torznab
// wire attr to carry it and no caller of this facade that would act on it.
func releaseToTorznab(rel schema.Release) torznab.Release {
	info := rel.Info
	out := torznab.Release{
		Title:      info.Title,
		GUID:       info.GUID,
		Link:       info.DownloadURL,
		CommentURL: info.InfoURL,
		Size:       info.SizeBytes,
		Seeders:    info.Seeders,
		Leechers:   info.Leechers,
		InfoHash:   info.InfoHash,
		MagnetURL:  info.MagnetURL,
	}
	if info.PublishedAt != nil {
		out.PubDate = info.PublishedAt.Time
	}
	if len(info.Categories) > 0 {
		out.Categories = make([]newznab.CategoryID, len(info.Categories))
		for i, c := range info.Categories {
			out.Categories[i] = newznab.CategoryID(c)
		}
	}
	if len(info.IDs) > 0 {
		// commonv1.ReleaseInfo.IDs and torznab.Release.IDs share the same
		// key vocabulary (tmdb/imdb/tvdb, the IMDb value "tt"-prefixed) --
		// see release_types.go's "Well-known keys of ReleaseInfo.IDs" and
		// torznab/release.go's Release.IDs doc comment -- so this is a copy,
		// not a translation.
		out.IDs = make(map[string]string, len(info.IDs))
		for k, v := range info.IDs {
			out.IDs[k] = v
		}
	}
	return out
}

// writeResults renders releases as a Torznab results feed.
func (s *Server) writeResults(ctx context.Context, w http.ResponseWriter, releases []schema.Release) {
	w.Header().Set("Content-Type", torznabContentType)
	rels := make([]torznab.Release, len(releases))
	for i, r := range releases {
		rels[i] = releaseToTorznab(r)
	}
	if err := torznab.WriteResults(w, rels); err != nil {
		// Headers (and very possibly some of the body) are already on the
		// wire by the time WriteResults can fail -- a client that hung up
		// mid-write, most often -- so there is nothing left to tell the
		// caller.
		logging.FromContext(ctx).Debug("facade: writing results failed", "err", err)
	}
}
