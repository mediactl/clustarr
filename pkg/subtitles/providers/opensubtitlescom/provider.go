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

package opensubtitlescom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

type searchResponse struct {
	Data []struct {
		Attributes struct {
			Language          string `json:"language"`
			HearingImpaired   bool   `json:"hearing_impaired"`
			ForeignPartsOnly  bool   `json:"foreign_parts_only"`
			FromTrusted       bool   `json:"from_trusted"`
			AITranslated      bool   `json:"ai_translated"`
			MachineTranslated bool   `json:"machine_translated"`
			DownloadCount     int    `json:"download_count"`
			Release           string `json:"release"`
			MoviehashMatch    bool   `json:"moviehash_match"`
			Uploader          struct {
				Name string `json:"name"`
			} `json:"uploader"`
			FeatureDetails featureDetails `json:"feature_details"`
			Files          []struct {
				FileID   int    `json:"file_id"`
				FileName string `json:"file_name"`
			} `json:"files"`
		} `json:"attributes"`
	} `json:"data"`
}

// featureDetails is the part of a search result's feature_details the
// match rules read (research note §4.3's response shape). Every field is
// nullable upstream: a movie's season_number and parent ids are null.
type featureDetails struct {
	Year          *int   `json:"year"`
	SeasonNumber  *int   `json:"season_number"`
	EpisodeNumber *int   `json:"episode_number"`
	IMDbID        *int64 `json:"imdb_id"`
	TMDbID        *int64 `json:"tmdb_id"`
	ParentIMDbID  *int64 `json:"parent_imdb_id"`
	ParentTMDbID  *int64 `json:"parent_tmdb_id"`
}

// Search implements subtitles.Provider.Search against the OpenSubtitles.com
// /subtitles endpoint (research note §4.3).
//
// An episode is searched by its show's ids as well as its own: Query.IDs'
// "parent_imdb" and "parent_tmdb" (the series' ids, which is what
// captionarr's fetch worker fills for an episode) become parent_imdb_id and
// parent_tmdb_id, alongside season_number and episode_number. Bazarr sends
// parent_imdb_id the same way (opensubtitlescom.py query()); without it an
// episode could be found by moviehash alone.
func (p *Provider) Search(ctx context.Context, q subtitles.Query) ([]subtitles.Candidate, error) {
	ctx, span := tracing.Start(ctx, "subtitles.opensubtitlescom.search")
	defer span.End()

	params := url.Values{}
	setID(params, "imdb_id", q.IDs["imdb"])
	setID(params, "tmdb_id", q.IDs["tmdb"])
	if q.Kind == common.MediaKindEpisode {
		setID(params, "parent_imdb_id", q.IDs["parent_imdb"])
		setID(params, "parent_tmdb_id", q.IDs["parent_tmdb"])
	}
	if q.Hash != "" {
		params.Set("moviehash", q.Hash)
	}
	if q.Season > 0 {
		params.Set("season_number", strconv.Itoa(q.Season))
	}
	if q.Episode > 0 {
		params.Set("episode_number", strconv.Itoa(q.Episode))
	}
	params.Set("type", osType(q.Kind))
	params.Set("ai_translated", "exclude")

	langs := make([]string, 0, len(q.Languages))
	hi, forced := "include", "exclude"
	for _, lk := range q.Languages {
		lang, isForced, isHI, err := subtitles.ParseLangKey(lk)
		if err != nil {
			return nil, err
		}
		langs = append(langs, strings.ToLower(lang))
		switch {
		case isForced:
			hi, forced = "exclude", "only"
		case isHI:
			hi, forced = "only", "exclude"
		}
	}
	params.Set("languages", strings.Join(langs, ","))
	params.Set("hearing_impaired", hi)
	params.Set("foreign_parts_only", forced)

	// url.Values.Encode sorts by key: Bazarr sorts the parameters too,
	// because the API caches on the exact query string and redirects an
	// unsorted one.
	resp, err := p.doAuthed(ctx, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, p.apiURL()+"/subtitles?"+params.Encode(), nil)
	})
	if err != nil {
		tracing.RecordError(span, err)
		var pe *subtitles.ProviderError
		if errors.As(err, &pe) {
			return nil, err
		}
		return nil, fmt.Errorf("subtitles: opensubtitlescom search: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		err := statusToProviderError(p.Name(), resp)
		tracing.RecordError(span, err)
		return nil, err
	}

	var sr searchResponse
	if err := decodeJSON(resp.Body, &sr); err != nil {
		return nil, fmt.Errorf("subtitles: opensubtitlescom search: decode: %w", err)
	}

	out := make([]subtitles.Candidate, 0, len(sr.Data))
	for _, d := range sr.Data {
		a := d.Attributes
		if len(a.Files) == 0 {
			continue
		}
		matches := featureMatches(q, a.FeatureDetails)
		if a.MoviehashMatch {
			matches[subtitles.MatchHash] = true
		}
		out = append(out, subtitles.Candidate{
			Provider: p.Name(), FetchID: strconv.Itoa(a.Files[0].FileID),
			Language: a.Language, HI: a.HearingImpaired, Forced: a.ForeignPartsOnly && !a.HearingImpaired,
			ReleaseInfo: a.Release, Matches: matches,
			Uploader: a.Uploader.Name, Trusted: a.FromTrusted,
			AITranslated: a.AITranslated, MachineTranslated: a.MachineTranslated, Downloads: a.DownloadCount,
		})
	}
	return out, nil
}

// featureMatches is OpenSubtitlesComSubtitle.get_matches
// (opensubtitlescom.py) for the identity half of a result, expressed in
// this package's Match keys. For an episode: series always (Bazarr adds it
// unconditionally -- the search itself was pinned to the show), season and
// episode when feature_details agrees with the query. For a movie: title
// always. Either way, year when the result's ids match the query's or its
// year equals the query's.
//
// Bazarr's id match (imdb_match) becomes the Match keys its score.py
// expands it to: the show's id matching (series_imdb_id) implies series and
// year; the episode's or movie's own id matching (imdb_id) implies
// everything identity can say -- for an episode that includes its season
// and episode. TMDB ids count as well as IMDb ones, since the query can
// carry either.
//
// The release-derived keys (source, release group, codecs) are left to the
// caller's subtitles.GuessMatches over Candidate.ReleaseInfo.
func featureMatches(q subtitles.Query, fd featureDetails) map[string]bool {
	m := map[string]bool{}
	own := sameID(fd.IMDbID, q.IDs["imdb"]) || sameID(fd.TMDbID, q.IDs["tmdb"])
	year := own || (fd.Year != nil && q.Year > 0 && *fd.Year == q.Year)

	if q.Kind == common.MediaKindEpisode {
		parent := sameID(fd.ParentIMDbID, q.IDs["parent_imdb"]) || sameID(fd.ParentTMDbID, q.IDs["parent_tmdb"])
		m[subtitles.MatchSeries] = true
		m[subtitles.MatchSeason] = own || (fd.SeasonNumber != nil && *fd.SeasonNumber == q.Season)
		m[subtitles.MatchEpisode] = own || (fd.EpisodeNumber != nil && *fd.EpisodeNumber == q.Episode)
		year = year || parent
	} else {
		m[subtitles.MatchTitle] = true
	}
	m[subtitles.MatchYear] = year
	for k, v := range m {
		if !v {
			delete(m, k)
		}
	}
	return m
}

// sameID reports whether a feature_details id equals a query id. The
// query's id is compared after the same normalisation setID sends it with.
func sameID(got *int64, want string) bool {
	if got == nil || want == "" {
		return false
	}
	n, ok := numericID(want)
	return ok && n == strconv.FormatInt(*got, 10)
}

// setID sets param to the id v, normalised the way Bazarr's
// sanitize_external_ids does ("tt0133093" -> "133093": no prefix, no
// leading zeros), because the API caches on the exact query string. An id
// that is not a number after the prefix is stripped is sent as given --
// what this client always did -- rather than silently dropped, since
// dropping an id widens the search to other titles.
func setID(params url.Values, param, v string) {
	if v == "" {
		return
	}
	if n, ok := numericID(v); ok {
		params.Set(param, n)
		return
	}
	params.Set(param, v)
}

// numericID strips an IMDb "tt" prefix and leading zeros from v and
// reports whether what is left is a decimal number.
func numericID(v string) (string, bool) {
	s := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(v)), "tt")
	if s == "" {
		return "", false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	s = strings.TrimLeft(s, "0")
	if s == "" {
		s = "0"
	}
	return s, true
}

func osType(kind common.MediaKind) string {
	if kind == common.MediaKindEpisode {
		return "episode"
	}
	return "movie"
}

type downloadResponse struct {
	Link     string `json:"link"`
	FileName string `json:"file_name"`
}

// Download implements subtitles.Provider.Download: it resolves c.FetchID
// (an OpenSubtitles file_id) to a short-lived download link via /download,
// then fetches that link.
func (p *Provider) Download(ctx context.Context, c subtitles.Candidate) ([]byte, string, error) {
	ctx, span := tracing.Start(ctx, "subtitles.opensubtitlescom.download")
	defer span.End()

	fileID, err := strconv.Atoi(c.FetchID)
	if err != nil {
		return nil, "", fmt.Errorf("subtitles: opensubtitlescom: invalid FetchID %q: %w", c.FetchID, err)
	}
	body, err := json.Marshal(map[string]any{"file_id": fileID, "sub_format": "srt"})
	if err != nil {
		return nil, "", fmt.Errorf("subtitles: opensubtitlescom download: encode request: %w", err)
	}
	resp, err := p.doAuthed(ctx, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL()+"/download", bytes.NewReader(body))
	})
	if err != nil {
		tracing.RecordError(span, err)
		var pe *subtitles.ProviderError
		if errors.As(err, &pe) {
			return nil, "", err
		}
		return nil, "", fmt.Errorf("subtitles: opensubtitlescom download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		err := statusToProviderError(p.Name(), resp)
		tracing.RecordError(span, err)
		return nil, "", err
	}

	var dr downloadResponse
	if err := decodeJSON(resp.Body, &dr); err != nil {
		return nil, "", fmt.Errorf("subtitles: opensubtitlescom download: decode: %w", err)
	}

	fileReq, err := http.NewRequestWithContext(ctx, http.MethodGet, dr.Link, nil)
	if err != nil {
		return nil, "", err
	}
	fileResp, err := p.cfg.HTTPClient.Do(fileReq)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, "", fmt.Errorf("subtitles: opensubtitlescom fetch link: %w", err)
	}
	defer func() { _ = fileResp.Body.Close() }()
	// Read one byte past the limit so a body exactly at the limit is
	// accepted while anything larger is detected without ever buffering
	// more than maxSubtitleBytes+1 bytes.
	raw, err := io.ReadAll(io.LimitReader(fileResp.Body, maxSubtitleBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("subtitles: opensubtitlescom read body: %w", err)
	}
	if len(raw) > maxSubtitleBytes {
		sizeErr := fmt.Errorf("%w: at least %d bytes", ErrResponseTooLarge, len(raw))
		tracing.RecordError(span, sizeErr)
		return nil, "", sizeErr
	}
	return raw, dr.FileName, nil
}
