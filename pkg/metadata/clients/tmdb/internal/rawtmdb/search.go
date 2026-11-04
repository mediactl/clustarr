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

// Adapted from github.com/cyruzin/golang-tmdb v1.9.4 search.go, find.go,
// collections.go and results.go (MIT, Copyright (c) 2019 Cyro Dubeux;
// LICENSE in this directory): the movie search, the lookup by an external
// id and the collection details.

package rawtmdb

import (
	"fmt"
	"net/url"
)

// SearchMovies is a page of a movie search.
type SearchMovies struct {
	PaginatedResultsMeta
	*SearchMoviesResults
}

// SearchMoviesResults is the body of SearchMovies.
type SearchMoviesResults struct {
	Results []MovieResult `json:"results"`
}

// MovieResult is a movie as a search lists it.
type MovieResult struct {
	ID               int64   `json:"id"`
	Title            string  `json:"title"`
	OriginalTitle    string  `json:"original_title"`
	OriginalLanguage string  `json:"original_language"`
	Overview         string  `json:"overview"`
	ReleaseDate      string  `json:"release_date"`
	PosterPath       string  `json:"poster_path"`
	BackdropPath     string  `json:"backdrop_path"`
	Popularity       float32 `json:"popularity"`
	GenreIDs         []int64 `json:"genre_ids"`
	Adult            bool    `json:"adult"`
	Video            bool    `json:"video"`
	VoteMetrics
}

// GetSearchMovies searches movies by title.
//
// https://developer.themoviedb.org/reference/search-movie
func (c *Client) GetSearchMovies(query string, urlOptions map[string]string) (*SearchMovies, error) {
	tmdbURL := fmt.Sprintf("%s%smovie?api_key=%s&query=%s%s", baseURL, searchURL, c.apiKey, url.QueryEscape(query), c.fmtOptions(urlOptions))
	searchMovies := SearchMovies{}
	if err := c.get(tmdbURL, &searchMovies); err != nil {
		return nil, err
	}
	return &searchMovies, nil
}

// FindByID is everything TMDB holds under an external id, such as an
// IMDb id: movies, people, TV shows, episodes and seasons.
type FindByID struct {
	MovieResults []struct {
		Adult            bool    `json:"adult"`
		BackdropPath     string  `json:"backdrop_path"`
		GenreIDs         []int64 `json:"genre_ids"`
		ID               int64   `json:"id"`
		OriginalLanguage string  `json:"original_language"`
		OriginalTitle    string  `json:"original_title"`
		Overview         string  `json:"overview"`
		PosterPath       string  `json:"poster_path"`
		ReleaseDate      string  `json:"release_date"`
		Title            string  `json:"title"`
		Video            bool    `json:"video"`
		Popularity       float32 `json:"popularity"`
		VoteMetrics
	} `json:"movie_results,omitempty"`
	PersonResults []struct {
		Adult    bool   `json:"adult"`
		Gender   int    `json:"gender"`
		Name     string `json:"name"`
		ID       int64  `json:"id"`
		KnownFor []struct {
			Adult            bool     `json:"adult,omitempty"`
			BackdropPath     string   `json:"backdrop_path"`
			FirstAirDate     string   `json:"first_air_date,omitempty"`
			MediaType        string   `json:"media_type"`
			Name             string   `json:"name,omitempty"`
			OriginalLanguage string   `json:"original_language"`
			OriginalName     string   `json:"original_name,omitempty"`
			OriginalTitle    string   `json:"original_title,omitempty"`
			OriginCountry    []string `json:"origin_country,omitempty"`
			Overview         string   `json:"overview"`
			Popularity       float32  `json:"popularity"`
			PosterPath       string   `json:"poster_path"`
			ReleaseDate      string   `json:"release_date,omitempty"`
			Title            string   `json:"title,omitempty"`
			Video            bool     `json:"video,omitempty"`
			VoteMetrics
		} `json:"known_for"`
		KnownForDepartment string  `json:"known_for_department"`
		ProfilePath        string  `json:"profile_path"`
		Popularity         float32 `json:"popularity"`
	} `json:"person_results,omitempty"`
	TvResults []struct {
		OriginalName     string   `json:"original_name"`
		ID               int64    `json:"id"`
		Name             string   `json:"name"`
		FirstAirDate     string   `json:"first_air_date"`
		PosterPath       string   `json:"poster_path"`
		GenreIDs         []int64  `json:"genre_ids"`
		OriginalLanguage string   `json:"original_language"`
		BackdropPath     string   `json:"backdrop_path"`
		Overview         string   `json:"overview"`
		OriginCountry    []string `json:"origin_country"`
		Popularity       float32  `json:"popularity"`
		VoteMetrics
	} `json:"tv_results,omitempty"`
	TvEpisodeResults []struct {
		AirDate        string `json:"air_date"`
		EpisodeNumber  int    `json:"episode_number"`
		ID             int64  `json:"id"`
		Name           string `json:"name"`
		Overview       string `json:"overview"`
		ProductionCode string `json:"production_code"`
		SeasonNumber   int    `json:"season_number"`
		ShowID         int64  `json:"show_id"`
		StillPath      string `json:"still_path"`
		VoteMetrics
	} `json:"tv_episode_results,omitempty"`
	TvSeasonResults []struct {
		AirDate      string `json:"air_date"`
		Name         string `json:"name"`
		ID           int64  `json:"id"`
		SeasonNumber int    `json:"season_number"`
		ShowID       int64  `json:"show_id"`
	} `json:"tv_season_results,omitempty"`
}

// GetFindByID looks an external id up (urlOptions names its
// external_source) across movies, TV shows and people.
//
// https://developer.themoviedb.org/reference/find-by-id
func (c *Client) GetFindByID(id string, urlOptions map[string]string) (*FindByID, error) {
	tmdbURL := fmt.Sprintf("%s/find/%s?api_key=%s%s", baseURL, id, c.apiKey, c.fmtOptions(urlOptions))
	findByID := FindByID{}
	if err := c.get(tmdbURL, &findByID); err != nil {
		return nil, err
	}
	return &findByID, nil
}

// CollectionDetails is a collection and the movies in it.
type CollectionDetails struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Overview     string `json:"overview"`
	PosterPath   string `json:"poster_path"`
	BackdropPath string `json:"backdrop_path"`
	Parts        []struct {
		Adult            bool    `json:"adult"`
		BackdropPath     string  `json:"backdrop_path"`
		GenreIDs         []int64 `json:"genre_ids"`
		ID               int64   `json:"id"`
		MediaType        string  `json:"media_type"`
		OriginalLanguage string  `json:"original_language"`
		OriginalTitle    string  `json:"original_title"`
		Overview         string  `json:"overview"`
		PosterPath       string  `json:"poster_path"`
		ReleaseDate      string  `json:"release_date"`
		Title            string  `json:"title"`
		Video            bool    `json:"video"`
		Popularity       float32 `json:"popularity"`
		VoteMetrics
	} `json:"parts"`
}

// GetCollectionDetails gets a collection by id.
//
// https://developer.themoviedb.org/reference/collection-details
func (c *Client) GetCollectionDetails(id int, urlOptions map[string]string) (*CollectionDetails, error) {
	tmdbURL := fmt.Sprintf("%s%s%d?api_key=%s%s", baseURL, collectionURL, id, c.apiKey, c.fmtOptions(urlOptions))
	collectionDetails := CollectionDetails{}
	if err := c.get(tmdbURL, &collectionDetails); err != nil {
		return nil, err
	}
	return &collectionDetails, nil
}
