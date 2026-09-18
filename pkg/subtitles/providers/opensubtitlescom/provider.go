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
			Files []struct {
				FileID   int    `json:"file_id"`
				FileName string `json:"file_name"`
			} `json:"files"`
		} `json:"attributes"`
	} `json:"data"`
}

// Search implements subtitles.Provider.Search against the OpenSubtitles.com
// /subtitles endpoint (research note §4.3).
func (p *Provider) Search(ctx context.Context, q subtitles.Query) ([]subtitles.Candidate, error) {
	if err := p.EnsureLoggedIn(ctx); err != nil {
		return nil, err
	}
	ctx, span := tracing.Start(ctx, "subtitles.opensubtitlescom.search")
	defer span.End()

	params := url.Values{}
	if v, ok := q.IDs["imdb"]; ok {
		params.Set("imdb_id", v)
	}
	if v, ok := q.IDs["tmdb"]; ok {
		params.Set("tmdb_id", v)
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

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/subtitles?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	p.setAuthHeaders(req)

	if err := p.cfg.Limiter.Wait(ctx); err != nil {
		return nil, err
	}
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, fmt.Errorf("subtitles: opensubtitlescom search: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		err := statusToProviderError(p.Name(), resp)
		tracing.RecordError(span, err)
		return nil, err
	}

	var sr searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("subtitles: opensubtitlescom search: decode: %w", err)
	}

	out := make([]subtitles.Candidate, 0, len(sr.Data))
	for _, d := range sr.Data {
		a := d.Attributes
		if len(a.Files) == 0 {
			continue
		}
		matches := map[string]bool{}
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
	if err := p.EnsureLoggedIn(ctx); err != nil {
		return nil, "", err
	}
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/download", bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	p.setAuthHeaders(req)

	if err := p.cfg.Limiter.Wait(ctx); err != nil {
		return nil, "", err
	}
	resp, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, "", fmt.Errorf("subtitles: opensubtitlescom download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		err := statusToProviderError(p.Name(), resp)
		tracing.RecordError(span, err)
		return nil, "", err
	}

	var dr downloadResponse
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
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
