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

// Adapted from github.com/cyruzin/golang-tmdb v1.9.4 types.go (MIT,
// Copyright (c) 2019 Cyro Dubeux; LICENSE in this directory).

package rawtmdb

// BelongsToCollection is the collection a movie belongs to.
type BelongsToCollection struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	PosterPath   string `json:"poster_path"`
	BackdropPath string `json:"backdrop_path"`
}

// ProductionCompany is a company involved in a production.
type ProductionCompany struct {
	Name          string `json:"name"`
	ID            int64  `json:"id"`
	LogoPath      string `json:"logo_path"`
	OriginCountry string `json:"origin_country"`
}

// Genre is a genre by id and name.
type Genre struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// ProductionCountry is a country involved in a production.
type ProductionCountry struct {
	Iso3166_1 string `json:"iso_3166_1"`
	Name      string `json:"name"`
}

// SpokenLanguage is a language spoken in a production.
type SpokenLanguage struct {
	Iso639_1 string `json:"iso_639_1"`
	Name     string `json:"name"`
}

// PaginatedResultsMeta is the paging of a paginated answer.
type PaginatedResultsMeta struct {
	Page         int64 `json:"page"`
	TotalResults int64 `json:"total_results"`
	TotalPages   int64 `json:"total_pages"`
}

// Season is a TV show's season.
type Season struct {
	AirDate      string  `json:"air_date"`
	EpisodeCount int     `json:"episode_count"`
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	Overview     string  `json:"overview"`
	PosterPath   string  `json:"poster_path"`
	SeasonNumber int     `json:"season_number"`
	VoteAverage  float32 `json:"vote_average,omitempty"`
	ShowID       int64   `json:"show_id,omitempty"`
}

// LastEpisodeToAir is a TV show's most recently aired episode.
type LastEpisodeToAir struct {
	AirDate        string `json:"air_date"`
	EpisodeNumber  int    `json:"episode_number"`
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Overview       string `json:"overview"`
	ProductionCode string `json:"production_code"`
	Runtime        int    `json:"runtime"`
	SeasonNumber   int    `json:"season_number"`
	ShowID         int64  `json:"show_id"`
	StillPath      string `json:"still_path"`
	VoteMetrics
}

// NextEpisodeToAir is a TV show's next scheduled episode.
type NextEpisodeToAir struct {
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
}

// Network is a television network.
type Network struct {
	Name          string `json:"name"`
	ID            int64  `json:"id"`
	LogoPath      string `json:"logo_path"`
	OriginCountry string `json:"origin_country"`
}

// CreatedBy is a person credited with creating a TV show.
type CreatedBy struct {
	ID          int64  `json:"id"`
	CreditID    string `json:"credit_id"`
	Name        string `json:"name"`
	Gender      int    `json:"gender"`
	ProfilePath string `json:"profile_path"`
}

// VoteMetrics is an item's vote count and average.
type VoteMetrics struct {
	VoteCount   int64   `json:"vote_count"`
	VoteAverage float32 `json:"vote_average"`
}

// ImageBase is what every image carries.
type ImageBase struct {
	AspectRatio float64 `json:"aspect_ratio"`
	FilePath    string  `json:"file_path"`
	Height      int     `json:"height"`
	Width       int     `json:"width"`
	VoteMetrics
}

// AlternativeTitle is a title an item carries in one country.
type AlternativeTitle struct {
	Iso3166_1 string `json:"iso_3166_1"`
	Title     string `json:"title"`
	Type      string `json:"type"`
}
