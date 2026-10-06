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
	"net/http"
	"sort"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

// itoa64 is strconv.FormatInt base 10, named for the call sites that build
// an external guid or a season title from an int64/int32 field.
func itoa64(v int64) string { return strconv.FormatInt(v, 10) }

// Metadata is the per-item object of research §5: one Movie, Series (a
// Plex "show"), season or episode. Every field research §5.1 marks
// "Required" is populated unconditionally, even with an empty string
// (originallyAvailableAt when no date is known -- the research note's
// example emits an empty string, which is how it satisfies "required");
// every optional field with no clustarr source is left at its zero value,
// which `omitempty` then drops (spec §D.5: "omitted, never blanked").
type Metadata struct {
	RatingKey             string `json:"ratingKey"`
	Key                   string `json:"key"`
	Guid                  string `json:"guid"`
	Type                  string `json:"type"`
	Title                 string `json:"title"`
	OriginallyAvailableAt string `json:"originallyAvailableAt"`

	Thumb string `json:"thumb,omitempty"`
	Art   string `json:"art,omitempty"`
	// ParentArt and GrandparentArt appear in Plex's example responses
	// though not its tables: the season's and the show's background.
	ParentArt      string `json:"parentArt,omitempty"`
	GrandparentArt string `json:"grandparentArt,omitempty"`
	ContentRating  string `json:"contentRating,omitempty"`
	OriginalTitle  string `json:"originalTitle,omitempty"`
	TitleSort      string `json:"titleSort,omitempty"`
	Year           int32  `json:"year,omitempty"`
	Summary        string `json:"summary,omitempty"`
	IsAdult        bool   `json:"isAdult,omitempty"`

	Duration int64  `json:"duration,omitempty"`
	Tagline  string `json:"tagline,omitempty"`
	Studio   string `json:"studio,omitempty"`
	Theme    string `json:"theme,omitempty"`

	ParentRatingKey string `json:"parentRatingKey,omitempty"`
	ParentKey       string `json:"parentKey,omitempty"`
	ParentGuid      string `json:"parentGuid,omitempty"`
	ParentType      string `json:"parentType,omitempty"`
	ParentTitle     string `json:"parentTitle,omitempty"`
	ParentThumb     string `json:"parentThumb,omitempty"`
	Index           *int32 `json:"index,omitempty"`

	GrandparentRatingKey string `json:"grandparentRatingKey,omitempty"`
	GrandparentKey       string `json:"grandparentKey,omitempty"`
	GrandparentGuid      string `json:"grandparentGuid,omitempty"`
	GrandparentType      string `json:"grandparentType,omitempty"`
	GrandparentTitle     string `json:"grandparentTitle,omitempty"`
	GrandparentThumb     string `json:"grandparentThumb,omitempty"`
	ParentIndex          *int32 `json:"parentIndex,omitempty"`

	Image         []Image         `json:"Image,omitempty"`
	OriginalImage []Image         `json:"OriginalImage,omitempty"`
	Genre         []Tag           `json:"Genre,omitempty"`
	Guids         []GuidRef       `json:"Guid,omitempty"`
	Collection    []CollectionRef `json:"Collection,omitempty"`
	Country       []Tag           `json:"Country,omitempty"`
	Network       []Tag           `json:"Network,omitempty"`
	Rating        []RatingObj     `json:"Rating,omitempty"`
	StudioTags    []Tag           `json:"Studio,omitempty"`
	Role          []PersonTag     `json:"Role,omitempty"`
	Director      []PersonTag     `json:"Director,omitempty"`
	Producer      []PersonTag     `json:"Producer,omitempty"`
	Writer        []PersonTag     `json:"Writer,omitempty"`
	Similar       []SimilarTag    `json:"Similar,omitempty"`

	// ChildCount, MinYear and MaxYear describe a collection (type
	// "collection"): how many of its movies the library holds and the span
	// of their years, as Plex's own metadata service answers one.
	ChildCount int          `json:"childCount,omitempty"`
	MinYear    int32        `json:"minYear,omitempty"`
	MaxYear    int32        `json:"maxYear,omitempty"`
	SeasonType []SeasonType `json:"SeasonType,omitempty"`

	Children *ChildrenContainer `json:"Children,omitempty"`
}

// ChildrenContainer is a show or season's own Children block (research
// §5.2): "it is expected that all the child objects be returned in this
// array" -- unlike /children and /grandchildren, it is never paged.
type ChildrenContainer struct {
	Size     int        `json:"size"`
	Metadata []Metadata `json:"Metadata"`
}

func int32ptr(v int32) *int32 { return &v }

// buildMovieMetadata builds a movie's Metadata object (spec §D.5).
func buildMovieMetadata(root rootDef, u urls, m *catalogv1.Movie) Metadata {
	key := RatingKey(m.UID)
	md := Metadata{
		RatingKey: key,
		Key:       metadataKey(key, false),
		Guid:      u.guid(root.identifier, metadataTypeMovie, key, moviePlexID(m)),
		Type:      metadataTypeMovie,
	}

	if meta := m.Status.Metadata; meta != nil {
		md.Title = meta.Title
		md.TitleSort = meta.SortTitle
		md.Summary = meta.Overview
		md.ContentRating = contentRating(meta.Certifications, catalogv1.Certification{Country: meta.CertificationCountry, Rating: meta.Certification}, u.loc)
		md.Year = meta.Year
		md.OriginallyAvailableAt = movieAvailableDate(meta)
		if meta.RuntimeMinutes > 0 {
			md.Duration = int64(meta.RuntimeMinutes) * 60000
		}
		md.Genre = genres(meta.Genres)
		md.Guids = guidRefs(meta.ExternalIDs)
		md.Rating = ratings(meta.Ratings)
		if meta.Collection != nil {
			md.Collection = []CollectionRef{movieCollectionRef(root, u, *meta.Collection)}
		}
		md.Tagline = meta.Tagline
		md.IsAdult = meta.Adult
		if len(meta.Studios) > 0 {
			md.Studio = meta.Studios[0]
		}
		md.StudioTags = tags(meta.Studios)
		md.Country = tags(meta.Countries)
		if u.loc.wantsOriginal(meta.OriginalLanguage, meta.Language) {
			// Asked in another language than the film's own (spec
			// 2026-09-30 §5.3): its original title, genres and images.
			md.OriginalTitle = meta.OriginalTitle
			md.Genre = withOriginalTags(md.Genre, meta.OriginalGenres)
			md.OriginalImage = originalImages(u, meta.Images, meta.OriginalLanguage, meta.OriginalTitle)
		}
	}

	af := artworkFor{kind: commonv1.MediaKindMovie, uid: m.UID, artwork: m.Status.Artwork, overlay: m.Status.Overlay}
	md.Thumb, md.Art = thumbAndArt(u.external, af)
	md.Image = withAlt(buildImages(u.external, af), md.Title)
	return md
}

// movieAvailableDate is originallyAvailableAt for a movie (spec §D.5): the
// earliest of inCinemas, digitalRelease and physicalRelease, "" when none is
// known.
func movieAvailableDate(meta *catalogv1.MovieMetadata) string {
	var earliest *metav1.Time
	for _, t := range []*metav1.Time{meta.InCinemas, meta.DigitalRelease, meta.PhysicalRelease} {
		if t == nil {
			continue
		}
		if earliest == nil || t.Before(earliest) {
			earliest = t
		}
	}
	if earliest == nil {
		return ""
	}
	return earliest.UTC().Format("2006-01-02")
}

// buildShowMetadata builds a series' Metadata object (a Plex "show", spec
// §D.5). includeChildren populates Children with one Metadata per season
// (research §5.2: "a show returns its seasons").
func buildShowMetadata(root rootDef, u urls, s *catalogv1.Series, idx *projection.Index, includeChildren bool) Metadata {
	key := RatingKey(s.UID)
	md := Metadata{
		RatingKey: key,
		Key:       metadataKey(key, true),
		Guid:      u.guid(root.identifier, metadataTypeShow, key, seriesPlexID(s)),
		Type:      metadataTypeShow,
	}

	if meta := s.Status.Metadata; meta != nil {
		md.Title = meta.Title
		md.TitleSort = meta.SortTitle
		md.Summary = meta.Overview
		md.ContentRating = contentRating(meta.Certifications, catalogv1.Certification{Country: meta.CertificationCountry, Rating: meta.Certification}, u.loc)
		md.Year = meta.Year
		if meta.FirstAired != nil {
			md.OriginallyAvailableAt = meta.FirstAired.UTC().Format("2006-01-02")
		}
		if meta.RuntimeMinutes > 0 {
			md.Duration = int64(meta.RuntimeMinutes) * 60000
		}
		md.Genre = genres(meta.Genres)
		md.Guids = guidRefs(meta.ExternalIDs)
		md.Network = networkTag(meta.Network)
		if len(meta.Networks) > 0 {
			md.Network = tags(meta.Networks)
		}
		md.Rating = ratings(meta.Ratings)
		md.Tagline = meta.Tagline
		if len(meta.Studios) > 0 {
			md.Studio = meta.Studios[0]
		}
		md.StudioTags = tags(meta.Studios)
		md.Country = tags(meta.Countries)
		if u.loc.wantsOriginal(meta.OriginalLanguage, meta.Language) {
			md.Genre = withOriginalTags(md.Genre, meta.OriginalGenres)
			md.OriginalImage = originalImages(u, meta.Images, meta.OriginalLanguage, meta.Title)
		}
	}
	md.SeasonType = seasonTypes(s)
	if s.Spec.TvdbID != 0 {
		// Plex's own TV theme host, by TVDB id: the only theme-music source
		// there is (verified 2026-09-30); movies have none.
		md.Theme = "https://tvthemes.plexapp.com/" + itoa64(s.Spec.TvdbID) + ".mp3"
	}

	af := artworkFor{kind: commonv1.MediaKindSeries, uid: s.UID, artwork: s.Status.Artwork, overlay: s.Status.Overlay}
	md.Thumb, md.Art = thumbAndArt(u.external, af)
	md.Image = withAlt(buildImages(u.external, af), md.Title)

	if includeChildren {
		md.Children = buildSeasonChildren(root, u, s, idx)
		if u.otherOrder(s) {
			md.Children = &ChildrenContainer{Metadata: []Metadata{}}
		}
	}
	return md
}

// buildSeasonChildren builds a show's Children block: one Metadata per
// season in status.seasons, sorted by number.
func buildSeasonChildren(root rootDef, u urls, s *catalogv1.Series, idx *projection.Index) *ChildrenContainer {
	seasons := append([]catalogv1.SeasonStatus(nil), s.Status.Seasons...)
	sort.Slice(seasons, func(i, j int) bool { return seasons[i].Number < seasons[j].Number })

	out := make([]Metadata, 0, len(seasons))
	for _, season := range seasons {
		md, ok := buildSeasonMetadata(root, u, s, season.Number, idx, false)
		if ok {
			out = append(out, md)
		}
	}
	return &ChildrenContainer{Size: len(out), Metadata: out}
}

// buildSeasonMetadata builds one season's Metadata object (spec §D.5). A
// season is not an object of its own -- its ratingKey is [SeasonKey] and its
// fields are derived from the owning Series -- so this reports false only
// when number names a season the Series' own status.seasons does not carry
// (an out-of-range request).
func buildSeasonMetadata(
	root rootDef, u urls, s *catalogv1.Series, number int32, idx *projection.Index, includeChildren bool,
) (Metadata, bool) {
	var season *catalogv1.SeasonStatus
	for i := range s.Status.Seasons {
		if s.Status.Seasons[i].Number == number {
			season = &s.Status.Seasons[i]
			break
		}
	}
	if season == nil {
		return Metadata{}, false
	}

	seriesKey := RatingKey(s.UID)
	key := SeasonKey(s.UID, number)
	title := ""
	if meta := s.Status.Metadata; meta != nil {
		title = meta.Title
	}

	md := Metadata{
		RatingKey:             key,
		Key:                   metadataKey(key, true),
		Guid:                  u.guid(root.identifier, metadataTypeSeason, key, seasonPlexID(s, number)),
		Type:                  metadataTypeSeason,
		Title:                 seasonTitle(number),
		OriginallyAvailableAt: seasonAvailableDate(idx.Episodes(s.UID), number),
		ParentRatingKey:       seriesKey,
		ParentKey:             metadataKey(seriesKey, true),
		ParentGuid:            u.guid(root.identifier, metadataTypeShow, seriesKey, seriesPlexID(s)),
		ParentType:            metadataTypeShow,
		ParentTitle:           title,
		Index:                 int32ptr(number),
	}
	if meta := s.Status.Metadata; meta != nil {
		md.Year = meta.Year
	}

	af := artworkFor{kind: commonv1.MediaKindSeries, uid: s.UID, artwork: s.Status.Artwork, overlay: s.Status.Overlay}
	md.Thumb, md.Art = thumbAndArt(u.external, af)
	md.ParentThumb = md.Thumb
	md.ParentArt = md.Art
	md.Image = withAlt(buildImages(u.external, af), md.Title)
	if poster := u.proxied(seasonPoster(s, number)); poster != "" {
		// The season's own poster (spec 2026-09-30 §5.1), through the photo
		// proxy since it is a provider URL with no artwork object.
		md.Thumb = poster
		md.Image = replaceImage(md.Image, plexImageCoverPoster, Image{Type: plexImageCoverPoster, URL: poster, Alt: md.Title})
	}

	if includeChildren {
		md.Children = buildEpisodeChildren(root, u, s, number, idx)
		if u.otherOrder(s) {
			md.Children = &ChildrenContainer{Metadata: []Metadata{}}
		}
	}
	return md, true
}

// seasonTitle is a season's own title (spec §D.5): "Season N". Plex's own
// example provider (research §5.1's SeasonType discussion) treats season 0
// as specials; this provider names it the same as any other number, since
// nothing in research or spec asks for a "Specials" special case.
func seasonTitle(number int32) string {
	if number == 0 {
		return "Specials"
	}
	return "Season " + itoa64(int64(number))
}

// seasonPoster is a season's own poster URL from status.metadata.
// seasonImages, "" when there is none.
func seasonPoster(s *catalogv1.Series, number int32) string {
	meta := s.Status.Metadata
	if meta == nil {
		return ""
	}
	// The series' own order's poster, else the aired order's; a poster
	// written before posters named their order is the aired order's.
	order := string(effectiveOrder(s))
	var aired string
	for _, img := range meta.SeasonImages {
		if img.Season != number || img.Type != catalogv1.ImageTypePoster {
			continue
		}
		o := img.Order
		if o == "" {
			o = string(catalogv1.EpisodeOrderOfficial)
		}
		if o == order {
			return img.URL
		}
		if o == string(catalogv1.EpisodeOrderOfficial) && aired == "" {
			aired = img.URL
		}
	}
	return aired
}

// withAlt sets every image's alt text to title, as Plex's example does.
func withAlt(images []Image, title string) []Image {
	for i := range images {
		images[i].Alt = title
	}
	return images
}

// replaceImage swaps the image of type t for img, or appends img.
func replaceImage(images []Image, t string, img Image) []Image {
	for i := range images {
		if images[i].Type == t {
			images[i] = img
			return images
		}
	}
	return append(images, img)
}

// seasonAvailableDate is a season's originallyAvailableAt (spec §D.5): the
// earliest airDate among its episodes, "" when none has aired yet.
func seasonAvailableDate(episodes []*catalogv1.Episode, seasonNumber int32) string {
	var earliest *metav1.Time
	for _, e := range episodes {
		if e.Spec.SeasonNumber != seasonNumber || e.Status.AirDate == nil {
			continue
		}
		if earliest == nil || e.Status.AirDate.Before(earliest) {
			earliest = e.Status.AirDate
		}
	}
	if earliest == nil {
		return ""
	}
	return earliest.UTC().Format("2006-01-02")
}

// buildEpisodeChildren builds a season's Children block: every episode of
// s owned by that season number, sorted by episode number.
func buildEpisodeChildren(root rootDef, u urls, s *catalogv1.Series, seasonNumber int32, idx *projection.Index) *ChildrenContainer {
	var episodes []*catalogv1.Episode
	for _, e := range idx.Episodes(s.UID) {
		if e.Spec.SeasonNumber == seasonNumber {
			episodes = append(episodes, e)
		}
	}
	sort.Slice(episodes, func(i, j int) bool { return episodes[i].Spec.EpisodeNumber < episodes[j].Spec.EpisodeNumber })

	out := make([]Metadata, len(episodes))
	for i, e := range episodes {
		out[i] = buildEpisodeMetadata(root, u, s, e)
	}
	return &ChildrenContainer{Size: len(out), Metadata: out}
}

// buildEpisodeMetadata builds one episode's Metadata object (spec §D.5).
func buildEpisodeMetadata(root rootDef, u urls, s *catalogv1.Series, e *catalogv1.Episode) Metadata {
	key := RatingKey(e.UID)
	seriesKey := RatingKey(s.UID)
	seasonKey := SeasonKey(s.UID, e.Spec.SeasonNumber)
	seriesTitle := ""
	if meta := s.Status.Metadata; meta != nil {
		seriesTitle = meta.Title
	}

	af := artworkFor{kind: commonv1.MediaKindSeries, uid: s.UID, artwork: s.Status.Artwork, overlay: s.Status.Overlay}
	parentThumb, seriesArt := thumbAndArt(u.external, af)
	if poster := u.proxied(seasonPoster(s, e.Spec.SeasonNumber)); poster != "" {
		parentThumb = poster
	}

	// An episode is answered under its plex:// GUID only inside a season
	// that is too. TVDB, whose numbering clustarr keeps, splits some runs
	// into seasons Plex keeps as one (an anime's s02e01 is Plex's s01e13),
	// and a plex:// episode under clustarr's season GUID contradicts Plex's
	// own hierarchy: PMS stopped refreshing every such episode (The Dangers
	// in My Heart season 2, 2026-10-06).
	seasonGuid := u.guid(root.identifier, metadataTypeSeason, seasonKey, seasonPlexID(s, e.Spec.SeasonNumber))
	episodeID := episodePlexID(e)
	if !strings.HasPrefix(seasonGuid, plexScheme+"://") {
		episodeID = ""
	}

	md := Metadata{
		RatingKey: key,
		Key:       metadataKey(key, false),
		Guid:      u.guid(root.identifier, metadataTypeEpisode, key, episodeID),
		Type:      metadataTypeEpisode,
		Title:     e.Status.Title,
		Summary:   e.Status.Overview,

		ParentRatingKey: seasonKey,
		ParentKey:       metadataKey(seasonKey, true),
		ParentGuid:      seasonGuid,
		ParentType:      metadataTypeSeason,
		ParentTitle:     seasonTitle(e.Spec.SeasonNumber),
		ParentThumb:     parentThumb,
		Index:           int32ptr(e.Spec.EpisodeNumber),

		GrandparentRatingKey: seriesKey,
		GrandparentKey:       metadataKey(seriesKey, true),
		GrandparentGuid:      u.guid(root.identifier, metadataTypeShow, seriesKey, seriesPlexID(s)),
		GrandparentType:      metadataTypeShow,
		GrandparentTitle:     seriesTitle,
		GrandparentThumb:     parentThumb,
		ParentIndex:          int32ptr(e.Spec.SeasonNumber),
	}
	if e.Status.AirDate != nil {
		md.OriginallyAvailableAt = e.Status.AirDate.UTC().Format("2006-01-02")
	}
	if e.Status.RuntimeMinutes > 0 {
		md.Duration = int64(e.Status.RuntimeMinutes) * 60000
	}
	md.ParentArt = seriesArt
	md.GrandparentArt = seriesArt
	if meta := s.Status.Metadata; meta != nil {
		md.ContentRating = contentRating(meta.Certifications, catalogv1.Certification{Country: meta.CertificationCountry, Rating: meta.Certification}, u.loc)
	}
	md.Image = buildEpisodeImages(u, e)
	if len(md.Image) > 0 {
		md.Thumb = md.Image[0].URL
	}
	return md
}

// handleMetadata answers GET {metadata key}/{ratingKey}: the full object
// (research §5, spec §D.2), honouring includeChildren for a show or season.
func (h *handler) handleMetadata(root rootDef) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idx, ok := h.index(w, r)
		if !ok {
			return
		}

		u := h.urlsFor(r, idx)
		md, ok := h.resolveMetadata(root, u, idx, r.PathValue("ratingKey"), r.URL.Query().Get("includeChildren") == "1")
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch md.Type {
		case metadataTypeMovie:
			h.enrichExtended(r.Context(), u, &md, commonv1.MediaKindMovie, types.UID(md.RatingKey), idx)
		case metadataTypeShow:
			h.enrichExtended(r.Context(), u, &md, commonv1.MediaKindSeries, types.UID(md.RatingKey), idx)
		case metadataTypeEpisode:
			h.enrichExtended(r.Context(), u, &md, commonv1.MediaKindEpisode, types.UID(md.RatingKey), idx)
		}

		writeMetadata(w, customizationOf(r, nil), MetadataContainer{
			Offset:     0,
			TotalSize:  1,
			Identifier: root.identifier,
			Size:       1,
			Metadata:   []Metadata{md},
		})
	}
}

// resolveMetadata looks ratingKey up in idx and builds its Metadata object,
// false when ratingKey names nothing this index knows or names a type root
// does not declare ([rootDef.declares]).
func (h *handler) resolveMetadata(root rootDef, u urls, idx *projection.Index, ratingKey string, includeChildren bool) (Metadata, bool) {
	uid, season, isSeason, ok := resolveKey(idx, ratingKey)
	if !ok {
		return Metadata{}, false
	}
	if isSeason {
		if !root.declares(typeSeason) {
			return Metadata{}, false
		}
		s, ok := idx.SeriesByUID(uid)
		if !ok {
			return Metadata{}, false
		}
		return buildSeasonMetadata(root, u, s, season, idx, includeChildren)
	}

	obj, ok := idx.ByUID(uid)
	if !ok || !root.declares(providerType(obj)) {
		return Metadata{}, false
	}
	switch v := obj.(type) {
	case *catalogv1.Movie:
		return buildMovieMetadata(root, u, v), true
	case *catalogv1.Series:
		return buildShowMetadata(root, u, v, idx, includeChildren), true
	case *catalogv1.Episode:
		s, ok := idx.SeriesOfEpisode(v.UID)
		if !ok {
			return Metadata{}, false
		}
		return buildEpisodeMetadata(root, u, s, v), true
	default:
		return Metadata{}, false
	}
}

// providerType is obj's numeric Plex provider type (spec §D.1): 1 for a
// Movie, 2 for a Series, 4 for an Episode, 0 for anything else. A season
// has no object; its ratingKey is checked against typeSeason directly.
func providerType(obj any) int {
	switch obj.(type) {
	case *catalogv1.Movie:
		return typeMovie
	case *catalogv1.Series:
		return typeShow
	case *catalogv1.Episode:
		return typeEpisode
	default:
		return 0
	}
}

// withOriginalTags pairs each genre with its original-language name, when
// the gateway recorded one per genre in the same order.
func withOriginalTags(genre []Tag, originals []string) []Tag {
	if len(originals) != len(genre) {
		return genre
	}
	for i := range genre {
		genre[i].OriginalTag = originals[i]
	}
	return genre
}

// originalImages are the item's images in its original language, through
// the photo proxy (they are provider URLs), as Plex's OriginalImage.
func originalImages(u urls, images []catalogv1.Image, lang, alt string) []Image {
	var out []Image
	for _, img := range images {
		if img.Language != lang {
			continue
		}
		t, ok := plexImageTypes[img.Type]
		if !ok {
			continue
		}
		if url := u.proxied(img.URL); url != "" {
			out = append(out, Image{Type: t, URL: url, Alt: alt})
		}
	}
	return out
}

// plexImageTypes maps the catalog's image types onto Plex's.
var plexImageTypes = map[catalogv1.ImageType]string{
	catalogv1.ImageTypePoster: plexImageCoverPoster,
	catalogv1.ImageTypeFanart: plexImageBackground,
	catalogv1.ImageTypeLogo:   plexImageClearLogo,
}
