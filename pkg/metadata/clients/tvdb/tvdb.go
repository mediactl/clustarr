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

// Package tvdb is an in-house metadata.SeriesProvider for TheTVDB v4 REST
// API (https://api4.thetvdb.com/v4). TheTVDB has no adopted Go client
// module (docs/research/metadata.md §2.2; the Phase B dependency pre-add
// list does not include one), so every request goes through net/http
// directly, including the apikey+pin -> bearer JWT login flow in auth.go.
package tvdb

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	xlanguage "golang.org/x/text/language"
	"golang.org/x/text/language/display"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

const defaultBaseURL = "https://api4.thetvdb.com/v4"

// waitOnLimiter is c.limiter.Wait, indirected through a package variable so
// a white-box test (limiter_internal_test.go) can count how many times
// doRequest waits on the limiter -- once per request attempt, including the
// retry after a 401 -- without this package's public constructor growing a
// test-only seam.
var waitOnLimiter = func(ctx context.Context, l *rate.Limiter) error {
	return l.Wait(ctx)
}

// Client is a metadata.SeriesProvider backed by TheTVDB v4.
type Client struct {
	authState

	http    *http.Client
	baseURL string
	apiKey  string
	pin     string
	limiter *rate.Limiter

	// language is the ISO 639-2 language titles are kept in (WithLocale;
	// titleLanguage when unset); region picks the certification.
	language string
	region   string
}

// WithLocale sets the language titles are kept in (ISO 639-1, the
// MetadataProvider's spec.language) and the region certifications are
// chosen for (spec.region), and returns c. It sets them in place rather
// than copying, because c holds its token cache behind a mutex; call it
// before the client is shared.
func (c *Client) WithLocale(language, region string) *Client {
	if b, err := xlanguage.ParseBase(strings.ToLower(language)); err == nil {
		c.language = b.ISO3()
	}
	c.region = strings.ToUpper(region)
	return c
}

// titleLang is the ISO 639-2 language titles are kept in.
func (c *Client) titleLang() string {
	if c.language == "" {
		return titleLanguage
	}
	return c.language
}

// New builds a Client. httpClient may be nil, in which case http.DefaultClient
// is used. baseURL overrides the default TheTVDB v4 host -- tests pass an
// httptest.Server URL; production callers pass "".
func New(apiKey, pin string, httpClient *http.Client, baseURL string, limiter *rate.Limiter) *Client {
	hc := httpClient
	if hc == nil {
		hc = http.DefaultClient
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		http:    hc,
		baseURL: baseURL,
		apiKey:  apiKey,
		pin:     pin,
		limiter: limiter,
	}
}

// Name identifies this provider for logging and metrics.
func (c *Client) Name() string { return "tvdb" }

// Capabilities describes what this provider supports.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{LookupBy: []string{metadata.KeyTVDB}, Changes: true}
}

// seriesExtendedResponse is TheTVDB v4's SeriesExtendedRecord envelope.
type seriesExtendedResponse struct {
	Data struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
		// Aliases are the other names TheTVDB knows the series by, each
		// with its ISO 639-2 language (SeriesExtendedRecord.aliases).
		Aliases []struct {
			Language string `json:"language"`
			Name     string `json:"name"`
		} `json:"aliases"`
		Overview   string `json:"overview"`
		FirstAired string `json:"firstAired"`
		LastAired  string `json:"lastAired"`
		Status     struct {
			Name string `json:"name"`
		} `json:"status"`
		OriginalCountry  string `json:"originalCountry"`
		OriginalLanguage string `json:"originalLanguage"`
		AverageRuntime   int32  `json:"averageRuntime"`
		AirsTime         string `json:"airsTime"`
		RemoteIDs        []struct {
			ID         string `json:"id"`
			Type       int    `json:"type"`
			SourceName string `json:"sourceName"`
		} `json:"remoteIds"`
		Genres []struct {
			Name string `json:"name"`
		} `json:"genres"`
		// Tags are the record's tag options, each in a named group
		// (Sub-Genre, Setting, Time Period, Geographic Location); only
		// the Sub-Genre group is a genre.
		Tags []struct {
			TagName string `json:"tagName"`
			Name    string `json:"name"`
		} `json:"tags"`
		// Image is the series' own poster; Artworks the rest, each typed
		// by /artwork/types' ids (artworkTypes, seasonArtworkTypes), a
		// season's naming it by seasonId.
		Image    string          `json:"image"`
		Artworks []artworkRecord `json:"artworks"`
		// Translations is what `?meta=translations` adds: the record's
		// name and overview in every language it has, the aliases among
		// them flagged isAlias. Name above is the original-language name,
		// so the English title is only ever here.
		Translations struct {
			Names     []translation `json:"nameTranslations"`
			Overviews []translation `json:"overviewTranslations"`
		} `json:"translations"`
		// The fields below serve the full Plex Metadata Response (spec
		// 2026-09-30); their names are the ones recorded in
		// test/data/metadata/tvdb/series-78804-extended.json.
		OriginalNetwork *struct {
			Name string `json:"name"`
		} `json:"originalNetwork"`
		LatestNetwork *struct {
			Name string `json:"name"`
		} `json:"latestNetwork"`
		Companies []struct {
			Name        string `json:"name"`
			CompanyType struct {
				CompanyTypeName string `json:"companyTypeName"`
			} `json:"companyType"`
		} `json:"companies"`
		ContentRatings []struct {
			Name    string `json:"name"`
			Country string `json:"country"`
		} `json:"contentRatings"`
		Characters []rawCharacter `json:"characters"`
		Seasons    []struct {
			ID     int64  `json:"id"`
			Number int32  `json:"number"`
			Image  string `json:"image"`
			Type   struct {
				Type string `json:"type"`
			} `json:"type"`
			// Name is the season's own name in the series' original
			// language ("Book 1: Water"; JoJo's "スティール・ボール・ラン"),
			// null for most seasons. NameTranslations lists the languages
			// it is translated into -- as one comma-joined string per
			// element, as TheTVDB sends it.
			Name             string   `json:"name"`
			NameTranslations []string `json:"nameTranslations"`
		} `json:"seasons"`
		SeasonTypes []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"seasonTypes"`
	} `json:"data"`
}

// rawCharacter is one credit of a series or episode record: a character
// played by a person, or a crew credit (Director, Writer, …) in peopleType.
type rawCharacter struct {
	Name         string `json:"name"`
	PersonName   string `json:"personName"`
	PeopleType   string `json:"peopleType"`
	Image        string `json:"image"`
	PersonImgURL string `json:"personImgURL"`
	Sort         int32  `json:"sort"`
}

// artworkRecord is one of a series record's artworks. Score is TheTVDB's
// ranking of it: 100,000 plus its votes once reviewed, 0 never scored.
type artworkRecord struct {
	Type     int    `json:"type"`
	Image    string `json:"image"`
	Language string `json:"language"`
	SeasonID int64  `json:"seasonId"`
	Score    int64  `json:"score"`
}

// rankArtworks orders arts best first, stably: the higher score, then the
// titles' language, no language, the series' original language, any other.
// TheTVDB's own image for a series or a season is no better than its score:
// for an anime it is its best English poster, often never scored (Cautious
// Hero's, a title card on black, scored 0 beside three scored key arts;
// owner, 2026-10-28), and taking it first put that card in Plex.
func rankArtworks(arts []artworkRecord, titleLang, original string) {
	rank := func(l string) int {
		switch l {
		case titleLang:
			return 0
		case "":
			return 1
		case original:
			return 2
		}
		return 3
	}
	slices.SortStableFunc(arts, func(a, b artworkRecord) int {
		if a.Score != b.Score {
			return cmp.Compare(b.Score, a.Score)
		}
		return cmp.Compare(rank(a.Language), rank(b.Language))
	})
}

// withOwnImage is arts with img, the record's own image of type t, as one
// more candidate when no artwork carries it: first, so it leads the ones
// never scored, as it led everything before ranking.
func withOwnImage(arts []artworkRecord, img string, t int, seasonID int64) []artworkRecord {
	if img == "" || slices.ContainsFunc(arts, func(a artworkRecord) bool { return a.Image == img }) {
		return arts
	}
	return append([]artworkRecord{{Type: t, Image: img, SeasonID: seasonID}}, arts...)
}

// artworkBase is TheTVDB's image host; an episode record's image is a path
// on it.
const artworkBase = "https://artworks.thetvdb.com"

// mapCharacters files TheTVDB's characters under the Plex people arrays.
func mapCharacters(chars []rawCharacter) []metadata.Person {
	var out []metadata.Person
	order := map[metadata.PersonKind]int32{}
	for _, ch := range chars {
		var kind metadata.PersonKind
		switch ch.PeopleType {
		case "Actor", "Guest Star":
			kind = metadata.PersonCast
		case "Director":
			kind = metadata.PersonDirector
		case "Writer":
			kind = metadata.PersonWriter
		case "Producer", "Executive Producer":
			kind = metadata.PersonProducer
		default:
			continue
		}
		p := metadata.Person{Kind: kind, Name: ch.PersonName, ImageURL: ch.PersonImgURL}
		if p.ImageURL == "" {
			p.ImageURL = ch.Image
		}
		if kind == metadata.PersonCast {
			p.Character = ch.Name
			p.Order = ch.Sort
		} else {
			p.Job = ch.PeopleType
			p.Order = order[kind]
			order[kind]++
		}
		out = append(out, p)
	}
	return out
}

// country converts TheTVDB's ISO 3166-1 alpha-3 code ("gbr") to alpha-2
// ("GB") and the English name ("United Kingdom"); an unknown code comes
// back upper-cased as both.
func country(code string) (alpha2, name string) {
	r, err := xlanguage.ParseRegion(code)
	if err != nil {
		up := strings.ToUpper(code)
		return up, up
	}
	return r.String(), display.English.Regions().Name(r)
}

// absoluteArtwork makes an image path on TheTVDB's host absolute.
func absoluteArtwork(u string) string {
	if strings.HasPrefix(u, "/") {
		return artworkBase + u
	}
	return u
}

// translation is one entry of a SeriesExtendedRecord's translations: a
// name or an overview in one ISO 639-3 language. An alias (isAlias) is a
// nickname the series is also known by in that language, never its title.
type translation struct {
	Name     string `json:"name"`
	Overview string `json:"overview"`
	Language string `json:"language"`
	IsAlias  bool   `json:"isAlias"`
}

// titleLanguage is the ISO 639-3 code of the language the catalog's titles
// and overviews are kept in. English, unconditionally: TheTVDB's record
// name is the original-language one, and a library page of 유부녀 킬러,
// デス・パレード and Machos Alfa is not what an English-speaking operator
// asked for. A per-provider language setting is a separate change.
const titleLanguage = "eng"

// primary returns the first non-alias entry in lang, or the zero value.
func primary(ts []translation, lang string) translation {
	for _, t := range ts {
		if t.Language == lang && !t.IsAlias {
			return t
		}
	}
	return translation{}
}

// artworkTypes maps TheTVDB v4's series artwork type ids (/artwork/types)
// onto the normalized image types; an id not listed here is left out. Icon
// (5) and Cinemagraph (20) have no image type, in the catalog or in Plex.
// 6 and 7 are a season's Banner and Poster (seasonArtworkTypes), which this
// map took for clear art and logo until 2026-10-21, so every series' Plex
// clearLogo was a season poster.
var artworkTypes = map[int]metadata.ImageType{
	1:  metadata.ImageTypeBanner,
	2:  metadata.ImageTypePoster,
	3:  metadata.ImageTypeFanart,
	22: metadata.ImageTypeClearart,
	23: metadata.ImageTypeLogo,
}

// seasonArtworkTypes maps a season's artwork type ids, filed under the
// season their seasonId names.
var seasonArtworkTypes = map[int]metadata.ImageType{
	6: metadata.ImageTypeBanner,
	7: metadata.ImageTypePoster,
}

// textArtworkTypes are the artworks that are their title's text, so one in
// another language than the titles' is no image of this series' title.
var textArtworkTypes = map[metadata.ImageType]bool{
	metadata.ImageTypeLogo:     true,
	metadata.ImageTypeClearart: true,
}

// seasonImagesPerKey caps a season's images of one type in one order: the
// season list's poster and TheTVDB's first artworks after it. Doctor Who
// has 23 posters for its first season alone, and seasonImages holds 400.
const seasonImagesPerKey = 3

// Series fetches a single series' extended record from
// subGenreTag is the tag group of TheTVDB's sub-genres ("Sub-Genre: Dark
// Comedy"), the one group of a record's tags that names a genre.
const subGenreTag = "Sub-Genre"

// /series/{id}/extended?meta=translations. Its title and overview are the
// record's primary English translation when there is one (titleLanguage),
// and the record's own original-language name and overview otherwise.
func (c *Client) Series(ctx context.Context, tvdbID string) (*metadata.Series, error) {
	ctx, span := tracing.Start(ctx, "metadata.tvdb.Series")
	defer span.End()
	logger := logging.FromContext(ctx)

	var raw seriesExtendedResponse
	if err := c.doRequest(ctx, http.MethodGet, "/series/"+tvdbID+"/extended?meta=translations", &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "tvdb: series fetch failed", "tvdb_id", tvdbID, "error", err)
		return nil, err
	}

	title, overview := raw.Data.Name, raw.Data.Overview
	if t := primary(raw.Data.Translations.Names, c.titleLang()); t.Name != "" {
		title = t.Name
	}
	if t := primary(raw.Data.Translations.Overviews, c.titleLang()); t.Overview != "" {
		overview = t.Overview
	}
	s := &metadata.Series{
		IDs:              metadata.ExternalIDs{metadata.KeyTVDB: strconv.FormatInt(raw.Data.ID, 10)},
		Title:            title,
		Overview:         overview,
		Status:           mapSeriesStatus(raw.Data.Status.Name),
		OriginalLanguage: originalLanguage(raw.Data.OriginalLanguage),
		Language:         originalLanguage(c.titleLang()),
		AirTime:          raw.Data.AirsTime,
		Runtime:          raw.Data.AverageRuntime,
	}
	for _, r := range raw.Data.RemoteIDs {
		switch r.SourceName {
		case "IMDB":
			s.IDs[metadata.KeyIMDb] = r.ID
		case "TheMovieDB.com":
			s.IDs[metadata.KeyTMDB] = r.ID
		}
	}
	for _, g := range raw.Data.Genres {
		s.Genres = append(s.Genres, g.Name)
	}
	for _, t := range raw.Data.Tags {
		if t.TagName == subGenreTag && t.Name != "" {
			s.SubGenres = append(s.SubGenres, t.Name)
		}
	}
	// The record's original-language name leads the alternate titles, so a
	// release named that way still matches the series once its title is
	// the English one; DistinctAltTitles drops it when it is the title.
	// TheTVDB's aliases follow, their language normalised as
	// OriginalLanguage is, repeats dropped. None carries a scene season:
	// that numbering comes from scene mappings, not from TheTVDB.
	aliases := make([]metadata.AltTitle, 0, len(raw.Data.Aliases)+1)
	aliases = append(aliases, metadata.AltTitle{Title: raw.Data.Name, Language: originalLanguage(raw.Data.OriginalLanguage)})
	for _, a := range raw.Data.Aliases {
		aliases = append(aliases, metadata.AltTitle{Title: a.Name, Language: originalLanguage(a.Language)})
	}
	s.AlternateTitles = metadata.DistinctAltTitles(s.Title, aliases)
	// Best first (rankArtworks): a consumer that takes the first poster --
	// the artwork fetch, the library page, Plex -- takes the best scored.
	arts := withOwnImage(slices.Clone(raw.Data.Artworks), raw.Data.Image, 2, 0)
	rankArtworks(arts, c.titleLang(), raw.Data.OriginalLanguage)
	for _, a := range arts {
		t, ok := artworkTypes[a.Type]
		if !ok || a.Image == "" {
			continue
		}
		img := metadata.Image{Type: t, URL: a.Image}
		if textArtworkTypes[t] {
			// Only in the titles' language or in none, as TMDB's
			// include_image_language keeps a logo.
			if a.Language != "" && a.Language != c.titleLang() {
				continue
			}
			img.Language = originalLanguage(a.Language)
		}
		s.Images = append(s.Images, img)
	}
	if t, ok := parseDate(raw.Data.FirstAired); ok {
		s.FirstAired = &t
		s.Year = int32(t.Year())
	}
	if t, ok := parseDate(raw.Data.LastAired); ok {
		s.LastAired = &t
	}
	mapSeriesExtended(&raw, s, c.region, c.titleLang())
	c.translateSeasonNames(ctx, &raw, s)

	logger.DebugContext(ctx, "tvdb: series fetched", "tvdb_id", tvdbID, "title", s.Title)
	return s, nil
}

// mapSeriesExtended maps what the full Plex Metadata Response needs from a
// series record: networks (original, latest, then every company TheTVDB
// types a Network), production companies, the country of origin, per-country
// content ratings, the cast, each stored order's season posters and banners
// and the season orderings TheTVDB offers.
func mapSeriesExtended(raw *seriesExtendedResponse, s *metadata.Series, region, titleLang string) {
	d := &raw.Data
	addNetwork := func(n string) {
		if n != "" && !slices.Contains(s.Networks, n) {
			s.Networks = append(s.Networks, n)
		}
	}
	if d.OriginalNetwork != nil {
		addNetwork(d.OriginalNetwork.Name)
	}
	if d.LatestNetwork != nil {
		addNetwork(d.LatestNetwork.Name)
	}
	for _, co := range d.Companies {
		switch co.CompanyType.CompanyTypeName {
		case "Network":
			addNetwork(co.Name)
		case "Production Company", "Studio":
			if !slices.Contains(s.Studios, co.Name) {
				s.Studios = append(s.Studios, co.Name)
			}
		}
	}
	if len(s.Networks) > 0 {
		s.Network = s.Networks[0]
	}
	origin := ""
	if d.OriginalCountry != "" {
		code, name := country(d.OriginalCountry)
		origin = code
		s.Countries = []string{name}
	}
	for _, cr := range d.ContentRatings {
		code, _ := country(cr.Country)
		if cr.Name != "" && !slices.ContainsFunc(s.Certifications, func(c metadata.Certification) bool { return c.Country == code }) {
			s.Certifications = append(s.Certifications, metadata.Certification{Country: code, Rating: cr.Name})
		}
	}
	cert := metadata.PickCertification(s.Certifications, region, origin)
	s.Certification, s.CertificationCountry = cert.Rating, cert.Country
	s.People = mapCharacters(d.Characters)
	type seasonKey struct {
		number int32
		order  string
		kind   metadata.ImageType
	}
	type seasonRef struct {
		number int32
		order  string
	}
	stored := map[int64]seasonRef{}
	per := map[seasonKey][]string{}
	add := func(se seasonRef, t metadata.ImageType, url string) {
		k := seasonKey{se.number, se.order, t}
		if url == "" || len(per[k]) >= seasonImagesPerKey || slices.Contains(per[k], url) {
			return
		}
		per[k] = append(per[k], url)
		n := se.number
		s.Images = append(s.Images, metadata.Image{Type: t, URL: url, Season: &n, SeasonOrder: se.order})
	}
	var arts []artworkRecord
	for _, a := range d.Artworks {
		if _, ok := seasonArtworkTypes[a.Type]; ok {
			arts = append(arts, a)
		}
	}
	for _, se := range d.Seasons {
		// The orders clustarr can store (spec.episodeOrder); TheTVDB's
		// alternate and regional orders never number a season here.
		switch se.Type.Type {
		case "official", "dvd", "absolute":
		default:
			continue
		}
		ref := seasonRef{se.Number, se.Type.Type}
		stored[se.ID] = ref
		arts = withOwnImage(arts, se.Image, 7, se.ID)
		if se.Name != "" {
			// The season's own name, in the original language; Series
			// translates it into the titles' language afterwards.
			s.Seasons = append(s.Seasons, metadata.Season{Number: se.Number, Order: metadata.SeasonOrder(se.Type.Type), Title: se.Name})
		}
	}
	// A season's banners and posters, its own list poster among them, best
	// first (rankArtworks): its first poster is the one the Plex provider
	// sends.
	rankArtworks(arts, titleLang, d.OriginalLanguage)
	for _, a := range arts {
		if ref, known := stored[a.SeasonID]; known {
			add(ref, seasonArtworkTypes[a.Type], a.Image)
		}
	}
	for _, st := range d.SeasonTypes {
		s.SeasonTypes = append(s.SeasonTypes, metadata.SeasonTypeRef{ID: st.Type, Name: st.Name})
	}
}

// seasonTitleFetches caps the season-name translations one Series fetch
// asks for, one request each: a long-running show has tens of seasons per
// order, and the limiter's budget is the episode lists' too.
const seasonTitleFetches = 40

// hasLanguage is whether a TheTVDB nameTranslations list names lang, each
// element being one language or a comma-joined run of them.
func hasLanguage(list []string, lang string) bool {
	for _, item := range list {
		for _, l := range strings.Split(item, ",") {
			if strings.TrimSpace(l) == lang {
				return true
			}
		}
	}
	return false
}

// translateSeasonNames gives each stored season of raw whose name TheTVDB
// has in the titles' language that name as its Title, keeping the original
// as OriginalTitle, when the series' original language is another: one
// request per season, seasonTitleFetches at most. A translation that
// cannot be fetched leaves the original as the title.
func (c *Client) translateSeasonNames(ctx context.Context, raw *seriesExtendedResponse, s *metadata.Series) {
	if raw.Data.OriginalLanguage == c.titleLang() {
		return
	}
	translated := 0
	for _, se := range raw.Data.Seasons {
		if se.Name == "" || !hasLanguage(se.NameTranslations, c.titleLang()) || translated >= seasonTitleFetches {
			continue
		}
		i := slices.IndexFunc(s.Seasons, func(x metadata.Season) bool {
			return x.Number == se.Number && string(x.Order) == se.Type.Type
		})
		if i < 0 {
			continue // an order clustarr does not store
		}
		translated++
		t, err := c.seasonName(ctx, se.ID)
		if err != nil {
			logging.FromContext(ctx).WarnContext(ctx, "tvdb: season name translation", "season_id", se.ID, "error", err)
			continue
		}
		if t != "" && t != se.Name {
			s.Seasons[i].Title, s.Seasons[i].OriginalTitle = t, se.Name
		}
	}
}

// seasonName is a season's name in the titles' language, from
// /seasons/{id}/translations/{lang} (verified live 2026-10-21: Avatar's
// season 8113 answers {"name": "Book 1: Water", "overview": …}).
func (c *Client) seasonName(ctx context.Context, seasonID int64) (string, error) {
	var raw struct {
		Data struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := c.doRequest(ctx, http.MethodGet, "/seasons/"+strconv.FormatInt(seasonID, 10)+"/translations/"+c.titleLang(), &raw); err != nil {
		return "", err
	}
	return strings.TrimSpace(raw.Data.Name), nil
}

// mapSeriesStatus maps TheTVDB's status.name to metadata.SeriesStatus.
func mapSeriesStatus(name string) metadata.SeriesStatus {
	switch name {
	case "Continuing":
		return metadata.SeriesStatusContinuing
	case "Ended":
		return metadata.SeriesStatusEnded
	case "Upcoming":
		return metadata.SeriesStatusUpcoming
	default:
		return ""
	}
}

// episodesResponse is TheTVDB v4's episode-list envelope, shared by every
// season-order endpoint (/series/{id}/episodes/{order}) and its translated
// form (/series/{id}/episodes/{order}/{lang}).
type episodesResponse struct {
	Data struct {
		Episodes []rawEpisode `json:"episodes"`
	} `json:"data"`
	// Links pages the list: v4 returns page_size (500) episodes per page
	// and names the next page in next, null on the last.
	Links struct {
		Next *string `json:"next"`
	} `json:"links"`
}

// rawEpisode is one entry of the list, kept with its id so a translated
// list can be filled from the untranslated one. A null name decodes to "".
type rawEpisode struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Aired          string `json:"aired"`
	Runtime        int32  `json:"runtime"`
	Overview       string `json:"overview"`
	SeasonNumber   int32  `json:"seasonNumber"`
	Number         int32  `json:"number"`
	AbsoluteNumber *int32 `json:"absoluteNumber"`
	// Image is the episode's still, a path on artworkBase.
	Image string `json:"image"`
}

// EpisodePeople fetches one episode's guest cast and crew from
// /episodes/{id}/extended (the full Plex Metadata Response's episode
// Role, Director and Writer).
func (c *Client) EpisodePeople(ctx context.Context, episodeID string) ([]metadata.Person, error) {
	ctx, span := tracing.Start(ctx, "metadata.tvdb.EpisodePeople")
	defer span.End()
	var raw struct {
		Data struct {
			Characters []rawCharacter `json:"characters"`
		} `json:"data"`
	}
	if err := c.doRequest(ctx, http.MethodGet, "/episodes/"+episodeID+"/extended", &raw); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	return mapCharacters(raw.Data.Characters), nil
}

// Episodes fetches a series' episode list under the given season order
// (one of SeasonOrder's values: default|official|dvd|absolute|alternate|
// regional -- passed through verbatim, the caller's to validate), with
// each episode's name and overview in titleLanguage: the untranslated
// list carries the original-language names (デス・ビリヤード), so the
// walk is of TheTVDB's translated list, /episodes/{order}/eng. Where
// TheTVDB has no English name it substitutes its own placeholder
// ("Episode 1"), which stands; a null name -- allowed by the API, though
// the placeholder usually covers it -- is filled from the untranslated
// list by episode id, walked once and only then.
func (c *Client) Episodes(ctx context.Context, tvdbID string, order string) ([]metadata.Episode, error) {
	ctx, span := tracing.Start(ctx, "metadata.tvdb.Episodes")
	defer span.End()
	logger := logging.FromContext(ctx)

	base := "/series/" + tvdbID + "/episodes/" + order
	translated, err := c.walkEpisodes(ctx, base+"/"+c.titleLang(), tvdbID, order)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if slices.ContainsFunc(translated, func(e rawEpisode) bool { return e.Name == "" }) {
		original, err := c.walkEpisodes(ctx, base, tvdbID, order)
		if err != nil {
			tracing.RecordError(span, err)
			return nil, err
		}
		byID := make(map[int64]rawEpisode, len(original))
		for _, e := range original {
			byID[e.ID] = e
		}
		for i := range translated {
			if translated[i].Name != "" {
				continue
			}
			if o, ok := byID[translated[i].ID]; ok {
				translated[i].Name = o.Name
				if translated[i].Overview == "" {
					translated[i].Overview = o.Overview
				}
			}
		}
	}

	episodes := make([]metadata.Episode, 0, len(translated))
	for _, e := range translated {
		ep := metadata.Episode{
			IDs:            metadata.ExternalIDs{metadata.KeyTVDB: strconv.FormatInt(e.ID, 10)},
			SeasonNumber:   e.SeasonNumber,
			EpisodeNumber:  e.Number,
			AbsoluteNumber: e.AbsoluteNumber,
			Title:          e.Name,
			Overview:       e.Overview,
			Runtime:        e.Runtime,
		}
		if t, ok := parseDate(e.Aired); ok {
			ep.AirDate = &t
		}
		if e.Image != "" {
			ep.Image = &metadata.Image{Type: metadata.ImageTypeScreenshot, URL: absoluteArtwork(e.Image)}
		}
		episodes = append(episodes, ep)
	}

	logger.DebugContext(ctx, "tvdb: episodes fetched", "tvdb_id", tvdbID, "order", order, "count", len(episodes))
	return episodes, nil
}

// walkEpisodes fetches every page of the episode list at path. The list
// is paged (episodesResponse.Links): every page is fetched, by number
// rather than by following links.next's own text, until next is null. An
// empty page ends the walk whatever next says, so a provider that kept
// naming one could not run the limiter's budget down on nothing.
func (c *Client) walkEpisodes(ctx context.Context, path, tvdbID, order string) ([]rawEpisode, error) {
	logger := logging.FromContext(ctx)
	var out []rawEpisode
	for page := 0; ; page++ {
		var raw episodesResponse
		if err := c.doRequest(ctx, http.MethodGet, path+"?page="+strconv.Itoa(page), &raw); err != nil {
			logger.ErrorContext(ctx, "tvdb: episodes fetch failed", "tvdb_id", tvdbID, "order", order, "path", path, "page", page, "error", err)
			return nil, err
		}
		out = append(out, raw.Data.Episodes...)
		if len(raw.Data.Episodes) == 0 || raw.Links.Next == nil || *raw.Links.Next == "" {
			break
		}
	}
	return out, nil
}

// updatesResponse is TheTVDB v4's /updates envelope.
type updatesResponse struct {
	Data []struct {
		RecordID int64 `json:"recordId"`
	} `json:"data"`
}

// Updates returns the record ids changed since since, across series,
// episodes and seasons.
func (c *Client) Updates(ctx context.Context, since time.Time) ([]string, error) {
	ctx, span := tracing.Start(ctx, "metadata.tvdb.Updates")
	defer span.End()
	logger := logging.FromContext(ctx)

	path := fmt.Sprintf("/updates?since=%d&type=series,episodes,seasons", since.Unix())
	var raw updatesResponse
	if err := c.doRequest(ctx, http.MethodGet, path, &raw); err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "tvdb: updates fetch failed", "error", err)
		return nil, err
	}

	ids := make([]string, 0, len(raw.Data))
	for _, u := range raw.Data {
		ids = append(ids, strconv.FormatInt(u.RecordID, 10))
	}
	return ids, nil
}

var _ metadata.SeriesProvider = (*Client)(nil)

// parseDate parses a TheTVDB "YYYY-MM-DD" date; an empty or malformed
// string reports ok=false rather than a zero time, so callers leave the
// corresponding *time.Time nil instead of storing a bogus 0001-01-01.
func parseDate(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// doRequest waits on the rate limiter, authenticates lazily on the first
// call, sets the bearer token, and on a 401 response authenticates exactly
// once more and retries the request once before giving up with
// metadata.ErrAuth. A 200 body is read through metadata.DecodeJSON's cap.
func (c *Client) doRequest(ctx context.Context, method, path string, out any) error {
	if err := waitOnLimiter(ctx, c.limiter); err != nil {
		return err
	}
	if c.currentToken() == "" {
		if err := c.authenticate(ctx); err != nil {
			return err
		}
	}

	resp, err := c.rawRequest(ctx, method, path)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		if err := c.authenticate(ctx); err != nil {
			return err
		}
		// The retried request is a second, distinct call against the
		// provider and must draw its own token from the limiter -- the
		// first Wait above only covers the request that came back 401.
		if err := waitOnLimiter(ctx, c.limiter); err != nil {
			return err
		}
		resp, err = c.rawRequest(ctx, method, path)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			_ = resp.Body.Close()
			return metadata.ErrAuth
		}
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		if err := metadata.DecodeJSON(resp.Body, metadata.MaxResponseBytes, out); err != nil {
			return fmt.Errorf("tvdb: %s: %w", path, err)
		}
		return nil
	case http.StatusNotFound:
		return metadata.ErrNotFound
	case http.StatusTooManyRequests:
		return &metadata.RateLimitedError{Provider: "tvdb", RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	default:
		return fmt.Errorf("tvdb: unexpected status %d for %s", resp.StatusCode, path)
	}
}

func (c *Client) rawRequest(ctx context.Context, method, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("tvdb: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.currentToken())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tvdb: %s %s: %w", method, path, err)
	}
	return resp, nil
}

// parseRetryAfter parses a Retry-After header value (seconds) into a
// duration, defaulting to zero when absent or malformed.
func parseRetryAfter(v string) time.Duration {
	seconds, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// originalLanguage converts TVDB's original-language code to BCP-47 at the
// provider boundary, where every other metadata client already speaks it.
//
// TVDB reports ISO 639-3 ("eng", "jpn") and this used to pass it through
// verbatim into Series.status.metadata.originalLanguage, a field every
// consumer reads as BCP-47 ("en", "ja"). pkg/decision failed open on the
// mismatch, so nothing was wrongly rejected -- but every TVDB-sourced series
// logged a warning per evaluation and its language conditions were inert.
// A code pkg/lang cannot normalise is passed through unchanged rather than
// dropped, so downstream keeps its existing fail-open handling of the unknown.
func originalLanguage(code string) string {
	if tag, ok := lang.Normalize(code); ok {
		return string(tag)
	}
	return code
}
