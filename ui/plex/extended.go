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
	"context"
	"net/http"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata/extended"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/projection"
)

// urls is how a builder makes an absolute URL: external is --external-url,
// the base of every /art URL; photo proxies a provider-hosted image (a
// person's photo, a season poster, an episode still) through the ui's
// signed /art/search, so nothing is hot-linked (ADR-0011). A nil photo
// drops such images.
type urls struct {
	external string
	photo    func(string) string
	// loc is the request's X-Plex-Country and X-Plex-Language.
	loc locale
	// episodeOrder is the order Plex asked seasons in, "" for the stored one.
	episodeOrder string
	// plexGUIDs is Options.PlexGUIDs.
	plexGUIDs bool
	// idx is the request's index, which a plex:// GUID is checked against.
	idx *projection.Index
}

// proxied is src through the photo proxy, "" when there is no proxy or no
// source.
func (u urls) proxied(src string) string {
	if u.photo == nil || src == "" {
		return ""
	}
	return u.photo(src)
}

// urlsFor is the urls every builder of one request uses, over idx.
func (h *handler) urlsFor(r *http.Request, idx *projection.Index) urls {
	return urls{
		external: h.opts.ExternalURL, photo: h.opts.PhotoURL, loc: localeOf(r),
		episodeOrder: r.URL.Query().Get("episodeOrder"),
		plexGUIDs:    h.opts.PlexGUIDs,
		idx:          idx,
	}
}

// PersonTag is one entry of a Role, Director, Producer or Writer array.
type PersonTag struct {
	Tag   string `json:"tag"`
	Thumb string `json:"thumb,omitempty"`
	Role  string `json:"role,omitempty"`
	Order int32  `json:"order,omitempty"`
}

// SimilarTag is one entry of the Similar array.
type SimilarTag struct {
	Guid string `json:"guid"`
	Tag  string `json:"tag,omitempty"`
}

// SeasonType is one entry of a show's SeasonType array.
type SeasonType struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Tag    string `json:"tag"`
	Title  string `json:"title"`
}

// tags is a string list as Tag objects, nil for none.
func tags(ss []string) []Tag {
	if len(ss) == 0 {
		return nil
	}
	out := make([]Tag, len(ss))
	for i, s := range ss {
		out[i] = Tag{Tag: s}
	}
	return out
}

// effectiveOrder mirrors episodeorder.EffectiveEpisodeOrder
// (app/catalog/episodeorder), which ui cannot import: the requested order,
// official when unset. Series type anime no longer forces absolute order
// (2026-10-06, anime dual-audio spec §3).
func effectiveOrder(s *catalogv1.Series) catalogv1.EpisodeOrder {
	if s.Spec.EpisodeOrder == "" {
		return catalogv1.EpisodeOrderOfficial
	}
	return s.Spec.EpisodeOrder
}

// orderNames are TheTVDB's own names for the orders clustarr stores, used
// until a refresh records the series' seasonTypes.
var orderNames = map[string]string{
	string(catalogv1.EpisodeOrderOfficial): "Aired Order",
	string(catalogv1.EpisodeOrderDVD):      "DVD Order",
	string(catalogv1.EpisodeOrderAbsolute): "Absolute Order",
}

// seasonTypes is a show's SeasonType array: the one order clustarr stores
// for the series (spec 2026-09-30 §5.1), named as TheTVDB names it.
func seasonTypes(s *catalogv1.Series) []SeasonType {
	id := string(effectiveOrder(s))
	name := orderNames[id]
	if name == "" {
		name = id
	}
	if meta := s.Status.Metadata; meta != nil {
		for _, st := range meta.SeasonTypes {
			if st.ID == id {
				name = st.Name
			}
		}
	}
	return []SeasonType{{ID: id, Source: "tvdb", Tag: name, Title: "TheTVDB (" + name + ")"}}
}

// ExtendedReadTimeout bounds the extended-document read on every metadata
// request, so a stalled bus costs Plex the item's people and similar
// titles, never the whole response.
const ExtendedReadTimeout = 2 * time.Second

// enrichExtended adds the people and similar titles from the item's
// extended-metadata document (pkg/metadata/extended). A missing document,
// or one that cannot be read, is no people -- never an error: Plex should
// still get everything else.
func (h *handler) enrichExtended(ctx context.Context, u urls, md *Metadata, kind commonv1.MediaKind, uid types.UID, idx *projection.Index) {
	if h.opts.Extended == nil {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, ExtendedReadTimeout)
	defer cancel()
	doc, ok, err := h.opts.Extended(rctx, kind, uid)
	if err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "plex: read the extended metadata document", "kind", kind, "error", err)
		return
	}
	if !ok {
		return
	}
	people := func(ps []extended.Person, crew bool) []PersonTag {
		if len(ps) == 0 {
			return nil
		}
		out := make([]PersonTag, len(ps))
		for i, p := range ps {
			role := p.Character
			if crew {
				role = p.Job
			}
			out[i] = PersonTag{Tag: p.Name, Thumb: u.proxied(p.Photo), Role: role, Order: p.Order}
		}
		return out
	}
	md.Role = people(doc.Role, false)
	md.Director = people(doc.Director, true)
	md.Writer = people(doc.Writer, true)
	md.Producer = people(doc.Producer, true)
	for _, s := range doc.Similar {
		md.Similar = append(md.Similar, SimilarTag{Guid: similarGuid(u, s, idx), Tag: s.Title})
	}
	if len(md.Collection) == 1 {
		withCollectionDoc(u, &md.Collection[0], doc.Collection)
	}
}

// similarGuid is a similar title's guid: clustarr's own, or its plex://
// GUID, when the title is in the catalog, so Plex can link it, else
// tmdb:// or tvdb://.
func similarGuid(u urls, s extended.Similar, idx *projection.Index) string {
	if s.TmdbID != 0 {
		if obj, ok := idx.ByTMDB(commonv1.MediaKindMovie, s.TmdbID); ok {
			if m, ok := obj.(*catalogv1.Movie); ok {
				return u.guid(moviesRoot.identifier, metadataTypeMovie, RatingKey(m.UID), moviePlexID(m))
			}
		}
		return "tmdb://" + strconv.FormatInt(s.TmdbID, 10)
	}
	if s.TvdbID != 0 {
		if sr, ok := idx.ByTVDB(s.TvdbID); ok {
			return u.guid(tvRoot.identifier, metadataTypeShow, RatingKey(sr.UID), seriesPlexID(sr))
		}
		return "tvdb://" + strconv.FormatInt(s.TvdbID, 10)
	}
	return ""
}

// otherOrder reports whether Plex asked for an episode order clustarr
// knows and does not store for s: the protocol says to return no season
// data then (spec 2026-09-30 §5.5). An id clustarr never advertises -- one
// left from the show's previous agent, or Plex's own -- is answered with
// the stored order, rather than a show with no seasons.
func (u urls) otherOrder(s *catalogv1.Series) bool {
	_, known := orderNames[u.episodeOrder]
	return known && u.episodeOrder != string(effectiveOrder(s))
}
