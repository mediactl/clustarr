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
		// Image is the series' own poster; Artworks the rest, each typed
		// by /artwork/types' ids (artworkTypes).
		Image    string `json:"image"`
		Artworks []struct {
			Type  int    `json:"type"`
			Image string `json:"image"`
		} `json:"artworks"`
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
			Number int32  `json:"number"`
			Image  string `json:"image"`
			Type   struct {
				Type string `json:"type"`
			} `json:"type"`
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

// artworkTypes maps TheTVDB v4's artwork type ids (/artwork/types) onto
// the normalized image types; an id not listed here is left out.
var artworkTypes = map[int]metadata.ImageType{
	1: metadata.ImageTypeBanner,
	2: metadata.ImageTypePoster,
	3: metadata.ImageTypeFanart,
	6: metadata.ImageTypeClearart,
	7: metadata.ImageTypeLogo,
}

// Series fetches a single series' extended record from
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
	// The series' own image is its poster and comes first, so a consumer
	// that takes the first poster (the library page) shows the one TheTVDB
	// itself leads with; the artworks follow in the order published.
	if raw.Data.Image != "" {
		s.Images = append(s.Images, metadata.Image{Type: metadata.ImageTypePoster, URL: raw.Data.Image})
	}
	for _, a := range raw.Data.Artworks {
		if t, ok := artworkTypes[a.Type]; ok && a.Image != "" {
			s.Images = append(s.Images, metadata.Image{Type: t, URL: a.Image})
		}
	}
	if t, ok := parseDate(raw.Data.FirstAired); ok {
		s.FirstAired = &t
		s.Year = int32(t.Year())
	}
	if t, ok := parseDate(raw.Data.LastAired); ok {
		s.LastAired = &t
	}
	mapSeriesExtended(&raw, s, c.region)

	logger.DebugContext(ctx, "tvdb: series fetched", "tvdb_id", tvdbID, "title", s.Title)
	return s, nil
}

// mapSeriesExtended maps what the full Plex Metadata Response needs from a
// series record: networks (original, latest, then every company TheTVDB
// types a Network), production companies, the country of origin, per-country
// content ratings, the cast, season posters for the aired order and the
// season orderings TheTVDB offers.
func mapSeriesExtended(raw *seriesExtendedResponse, s *metadata.Series, region string) {
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
	for _, se := range d.Seasons {
		if se.Type.Type != "official" || se.Image == "" {
			continue
		}
		n := se.Number
		s.Images = append(s.Images, metadata.Image{Type: metadata.ImageTypePoster, URL: se.Image, Season: &n})
	}
	for _, st := range d.SeasonTypes {
		s.SeasonTypes = append(s.SeasonTypes, metadata.SeasonTypeRef{ID: st.Type, Name: st.Name})
	}
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
