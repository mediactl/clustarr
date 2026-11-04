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

// Adapted from github.com/cyruzin/golang-tmdb v1.9.4 movies.go and
// results.go (MIT, Copyright (c) 2019 Cyro Dubeux; LICENSE in this
// directory): the movie details request and the seven sub-resources the
// caller appends to it.

package rawtmdb

import "fmt"

// MovieDetails is a movie's primary information, with the sub-resources
// named in append_to_response filled in beside it.
type MovieDetails struct {
	Adult               bool                `json:"adult"`
	BackdropPath        string              `json:"backdrop_path"`
	BelongsToCollection BelongsToCollection `json:"belongs_to_collection"`
	Budget              int64               `json:"budget"`
	Genres              []Genre             `json:"genres"`
	Homepage            string              `json:"homepage"`
	ID                  int64               `json:"id"`
	IMDbID              string              `json:"imdb_id"`
	OriginalLanguage    string              `json:"original_language"`
	OriginalTitle       string              `json:"original_title"`
	Overview            string              `json:"overview"`
	Popularity          float32             `json:"popularity"`
	PosterPath          string              `json:"poster_path"`
	OriginCountry       []string            `json:"origin_country"`
	ProductionCompanies []ProductionCompany `json:"production_companies"`
	ProductionCountries []ProductionCountry `json:"production_countries"`
	ReleaseDate         string              `json:"release_date"`
	Revenue             int64               `json:"revenue"`
	Runtime             int                 `json:"runtime"`
	SpokenLanguages     []SpokenLanguage    `json:"spoken_languages"`
	Status              string              `json:"status"`
	Tagline             string              `json:"tagline"`
	Title               string              `json:"title"`
	Video               bool                `json:"video"`
	VoteMetrics
	*MovieAlternativeTitlesAppend
	*MovieCreditsAppend
	*MovieExternalIDsAppend
	*MovieImagesAppend
	*MovieKeywordsAppend
	*MovieReleaseDatesAppend
	*MovieRecommendationsAppend
}

// MovieAlternativeTitlesAppend is alternative_titles in append_to_response.
type MovieAlternativeTitlesAppend struct {
	AlternativeTitles *MovieAlternativeTitles `json:"alternative_titles,omitempty"`
}

// MovieCreditsAppend is credits in append_to_response.
type MovieCreditsAppend struct {
	Credits struct {
		*MovieCredits
	} `json:"credits"`
}

// MovieExternalIDsAppend is external_ids in append_to_response.
type MovieExternalIDsAppend struct {
	*MovieExternalIDs `json:"external_ids,omitempty"`
}

// MovieImagesAppend is images in append_to_response.
type MovieImagesAppend struct {
	Images *MovieImages `json:"images,omitempty"`
}

// MovieKeywordsAppend is keywords in append_to_response.
type MovieKeywordsAppend struct {
	Keywords struct {
		*MovieKeywords
	} `json:"keywords"`
}

// MovieReleaseDatesAppend is release_dates in append_to_response.
type MovieReleaseDatesAppend struct {
	ReleaseDates *MovieReleaseDates `json:"release_dates,omitempty"`
}

// MovieRecommendationsAppend is recommendations in append_to_response.
type MovieRecommendationsAppend struct {
	Recommendations *MovieRecommendations `json:"recommendations,omitempty"`
}

// MovieAlternativeTitles is a movie's titles by country.
type MovieAlternativeTitles struct {
	ID     int                `json:"id"`
	Titles []AlternativeTitle `json:"titles"`
}

// MovieCredits is a movie's cast and crew.
type MovieCredits struct {
	ID   int64 `json:"id,omitempty"`
	Cast []struct {
		Adult              bool    `json:"adult"`
		CastID             int64   `json:"cast_id"`
		Character          string  `json:"character"`
		CreditID           string  `json:"credit_id"`
		Gender             int     `json:"gender"`
		ID                 int64   `json:"id"`
		KnownForDepartment string  `json:"known_for_department"`
		Name               string  `json:"name"`
		Order              int     `json:"order"`
		OriginalName       string  `json:"original_name"`
		Popularity         float32 `json:"popularity"`
		ProfilePath        string  `json:"profile_path"`
	} `json:"cast"`
	Crew []struct {
		Adult              bool    `json:"adult"`
		CreditID           string  `json:"credit_id"`
		Department         string  `json:"department"`
		Gender             int     `json:"gender"`
		ID                 int64   `json:"id"`
		Job                string  `json:"job"`
		KnownForDepartment string  `json:"known_for_department"`
		Name               string  `json:"name"`
		OriginalName       string  `json:"original_name"`
		Popularity         float32 `json:"popularity"`
		ProfilePath        string  `json:"profile_path"`
	} `json:"crew"`
}

// MovieExternalIDs is a movie's ids on other sites.
type MovieExternalIDs struct {
	IMDbID      string `json:"imdb_id"`
	FacebookID  string `json:"facebook_id"`
	InstagramID string `json:"instagram_id"`
	TwitterID   string `json:"twitter_id"`
	WikiDataID  string `json:"wikidata_id,omitempty"`
	ID          int64  `json:"id,omitempty"`
}

// MovieImage is one of a movie's images.
type MovieImage struct {
	ImageBase
	Iso639_1 string `json:"iso_639_1"`
}

// MovieImages is a movie's backdrops, logos and posters.
type MovieImages struct {
	ID        int64        `json:"id,omitempty"`
	Backdrops []MovieImage `json:"backdrops"`
	Logos     []MovieImage `json:"logos"`
	Posters   []MovieImage `json:"posters"`
}

// MovieKeywords is the keywords added to a movie.
type MovieKeywords struct {
	ID       int64 `json:"id,omitempty"`
	Keywords []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"keywords"`
}

// MovieReleaseDates is a movie's release dates and certifications by country.
type MovieReleaseDates struct {
	ID int64 `json:"id,omitempty"`
	*MovieReleaseDatesResults
}

// MovieReleaseDatesResults is the body of MovieReleaseDates.
type MovieReleaseDatesResults struct {
	Results []struct {
		Iso3166_1    string `json:"iso_3166_1"`
		ReleaseDates []struct {
			Certification string `json:"certification"`
			Iso639_1      string `json:"iso_639_1"`
			ReleaseDate   string `json:"release_date"`
			Type          int    `json:"type"`
			Note          string `json:"note"`
		} `json:"release_dates"`
	} `json:"results"`
}

// MovieRecommendations is the movies recommended beside a movie.
type MovieRecommendations struct {
	*MovieRecommendationsResults
	PaginatedResultsMeta
}

// MovieRecommendationsResults is the body of MovieRecommendations.
type MovieRecommendationsResults struct {
	Results []struct {
		PosterPath       string  `json:"poster_path"`
		Adult            bool    `json:"adult"`
		Overview         string  `json:"overview"`
		ReleaseDate      string  `json:"release_date"`
		GenreIDs         []int64 `json:"genre_ids"`
		ID               int64   `json:"id"`
		OriginalTitle    string  `json:"original_title"`
		OriginalLanguage string  `json:"original_language"`
		Title            string  `json:"title"`
		BackdropPath     string  `json:"backdrop_path"`
		Popularity       float32 `json:"popularity"`
		Video            bool    `json:"video"`
		VoteMetrics
	} `json:"results"`
}

// GetMovieDetails gets the primary information about a movie.
//
// https://developer.themoviedb.org/reference/movie-details
func (c *Client) GetMovieDetails(id int, urlOptions map[string]string) (*MovieDetails, error) {
	tmdbURL := fmt.Sprintf("%s%s%d?api_key=%s%s", baseURL, movieURL, id, c.apiKey, c.fmtOptions(urlOptions))
	movieDetails := MovieDetails{}
	if err := c.get(tmdbURL, &movieDetails); err != nil {
		return nil, err
	}
	return &movieDetails, nil
}
