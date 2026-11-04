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

// Adapted from github.com/cyruzin/golang-tmdb v1.9.4 tv.go and results.go
// (MIT, Copyright (c) 2019 Cyro Dubeux; LICENSE in this directory): the
// TV details request and the one sub-resource the caller appends to it.

package rawtmdb

import "fmt"

// TVDetails is a TV show's primary information, with the keywords filled
// in beside it when append_to_response names them.
type TVDetails struct {
	BackdropPath        string              `json:"backdrop_path"`
	CreatedBy           []CreatedBy         `json:"created_by"`
	EpisodeRunTime      []int               `json:"episode_run_time"`
	FirstAirDate        string              `json:"first_air_date"`
	Genres              []Genre             `json:"genres"`
	Homepage            string              `json:"homepage"`
	ID                  int64               `json:"id"`
	InProduction        bool                `json:"in_production"`
	Languages           []string            `json:"languages"`
	LastAirDate         string              `json:"last_air_date"`
	Name                string              `json:"name"`
	LastEpisodeToAir    LastEpisodeToAir    `json:"last_episode_to_air"`
	NextEpisodeToAir    NextEpisodeToAir    `json:"next_episode_to_air"`
	Networks            []Network           `json:"networks"`
	NumberOfEpisodes    int                 `json:"number_of_episodes"`
	NumberOfSeasons     int                 `json:"number_of_seasons"`
	OriginCountry       []string            `json:"origin_country"`
	OriginalLanguage    string              `json:"original_language"`
	OriginalName        string              `json:"original_name"`
	Overview            string              `json:"overview"`
	Popularity          float32             `json:"popularity"`
	PosterPath          string              `json:"poster_path"`
	ProductionCompanies []ProductionCompany `json:"production_companies"`
	ProductionCountries []ProductionCountry `json:"production_countries"`
	Seasons             []Season            `json:"seasons"`
	Status              string              `json:"status"`
	Tagline             string              `json:"tagline"`
	Type                string              `json:"type"`
	VoteMetrics
	*TVKeywordsAppend
}

// TVKeywordsAppend is keywords in append_to_response.
type TVKeywordsAppend struct {
	Keywords struct {
		*TVKeywords
	} `json:"keywords"`
}

// TVKeywords is the keywords added to a TV show.
type TVKeywords struct {
	ID int64 `json:"id,omitempty"`
	*TVKeywordsResults
}

// TVKeywordsResults is the body of TVKeywords.
type TVKeywordsResults struct {
	Results []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"results"`
}

// GetTVDetails gets the primary information about a TV show.
//
// https://developer.themoviedb.org/reference/tv-series-details
func (c *Client) GetTVDetails(id int, urlOptions map[string]string) (*TVDetails, error) {
	tmdbURL := fmt.Sprintf("%s%s%d?api_key=%s%s", baseURL, tvURL, id, c.apiKey, c.fmtOptions(urlOptions))
	tvDetails := TVDetails{}
	if err := c.get(tmdbURL, &tvDetails); err != nil {
		return nil, err
	}
	return &tvDetails, nil
}
