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

// Package plexgrid is the client for Plex's guide service grid
// (epg.provider.plex.tv /grid/bulk) and its reduction to xmltv.Listings
// (the custom-guides design's §2.1 and §5.3).
package plexgrid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

// BaseURL is Plex's guide service.
const BaseURL = "https://epg.provider.plex.tv"

// MaxBodyBytes caps a grid answer.
const MaxBodyBytes = 32 << 20

var (
	// ErrResponseTooLarge means the answer ran past MaxBodyBytes.
	ErrResponseTooLarge = errors.New("plexgrid: response too large")
	// ErrNotFound means the service has no such channel.
	ErrNotFound = errors.New("plexgrid: not found")
)

// RateLimitedError is the service's 429.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("plexgrid: rate limited, retry after %s", e.RetryAfter)
}

// Client asks the grid. The caller owns the limiter; nil waits for none.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
	Limiter interface{ Wait(context.Context) error }
}

// Programme is one airing reduced to the unified listing.
type Programme struct {
	Start, Stop time.Time
	xmltv.Listing
}

// Item is the decoded shape of one grid item: only the fields Reduce reads.
type Item struct {
	Type                 string  `json:"type"`
	GUID                 string  `json:"guid"`
	RatingKey            string  `json:"ratingKey"`
	Title                string  `json:"title"`
	Summary              string  `json:"summary"`
	Year                 int     `json:"year"`
	Index                int     `json:"index"`
	ParentIndex          int     `json:"parentIndex"`
	ParentGUID           string  `json:"parentGuid"`
	GrandparentTitle     string  `json:"grandparentTitle"`
	GrandparentGUID      string  `json:"grandparentGuid"`
	GrandparentRatingKey string  `json:"grandparentRatingKey"`
	GrandparentThumb     string  `json:"grandparentThumb"`
	GrandparentArt       string  `json:"grandparentArt"`
	Thumb                string  `json:"thumb"`
	Art                  string  `json:"art"`
	ContentRating        string  `json:"contentRating"`
	Duration             int64   `json:"duration"`
	OriginallyAvailable  string  `json:"originallyAvailableAt"`
	Genre                []Tag   `json:"Genre"`
	Team                 []Tag   `json:"Team"`
	Media                []Media `json:"Media"`
}

// Tag is a genre or team.
type Tag struct {
	Tag string `json:"tag"`
}

// Media is one airing.
type Media struct {
	BeginsAt int64 `json:"beginsAt"`
	EndsAt   int64 `json:"endsAt"`
	Duration int64 `json:"duration"` // milliseconds
	Live     bool  `json:"live"`
	Premiere bool  `json:"premiere"`
}

type answer struct {
	MediaContainer struct {
		Metadata []Item `json:"Metadata"`
	} `json:"MediaContainer"`
}

// Grid fetches the channel's airings between the two days (UTC dates) and
// reduces them, sorted by start.
func (c *Client) Grid(ctx context.Context, gridKey string, from, to time.Time) ([]Programme, error) {
	if c.Limiter != nil {
		if err := c.Limiter.Wait(ctx); err != nil {
			return nil, err
		}
	}
	base := c.BaseURL
	if base == "" {
		base = BaseURL
	}
	q := url.Values{}
	q.Set("channelGridKey", gridKey)
	q.Set("startDate", from.UTC().Format("2006-01-02"))
	q.Set("endDate", to.UTC().Format("2006-01-02"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/grid/bulk?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Plex-Token", c.Token)
	req.Header.Set("Accept", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("plexgrid: grid %s: %w", gridKey, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrNotFound
	case http.StatusTooManyRequests:
		secs, _ := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After")))
		secs = max(secs, 0)
		return nil, &RateLimitedError{RetryAfter: time.Duration(secs) * time.Second}
	default:
		return nil, fmt.Errorf("plexgrid: grid %s: %s", gridKey, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("plexgrid: read grid %s: %w", gridKey, err)
	}
	if len(body) > MaxBodyBytes {
		return nil, ErrResponseTooLarge
	}
	var a answer
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, fmt.Errorf("plexgrid: decode grid %s: %w", gridKey, err)
	}
	return Reduce(a.MediaContainer.Metadata), nil
}

// Reduce turns items into one Programme per airing, sorted by start.
func Reduce(items []Item) []Programme {
	var out []Programme
	for _, it := range items {
		l := listing(it)
		for _, m := range it.Media {
			end := m.EndsAt
			if end <= m.BeginsAt {
				if m.Duration <= 0 {
					continue
				}
				end = m.BeginsAt + m.Duration/1000
				if end <= m.BeginsAt {
					continue
				}
			}
			p := Programme{Start: time.Unix(m.BeginsAt, 0).UTC(), Stop: time.Unix(end, 0).UTC(), Listing: l}
			p.Categories = slices.Clone(l.Categories)
			p.Live = m.Live
			p.Premiere = m.Premiere
			p.New = m.Premiere
			p.PreviouslyShown = !m.Premiere
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

func listing(it Item) xmltv.Listing {
	l := xmltv.Listing{
		Kind:     xmltv.KindEpisode,
		Title:    it.Title,
		Desc:     it.Summary,
		PlexGUID: it.GUID,
		Poster:   it.Thumb,
		Backdrop: it.Art,
		Rating:   it.ContentRating,

		LengthMinutes:         int(it.Duration / 60000),
		OriginallyAvailableAt: datePart(it.OriginallyAvailable),
	}
	if it.Type == "movie" {
		l.Kind = xmltv.KindMovie
	} else {
		l.Title = it.GrandparentTitle
		l.SubTitle = it.Title
		l.Season, l.Episode = it.ParentIndex, it.Index
		l.Poster, l.Backdrop, l.Still = it.GrandparentThumb, it.GrandparentArt, it.Thumb
		l.PlexShowGUID = it.GrandparentGUID
		switch {
		case it.ParentGUID != "":
			l.PlexSeasonGUID = it.ParentGUID
		case it.GrandparentRatingKey != "" && it.ParentIndex > 0:
			l.PlexSeasonGUID = "plex://season/" + it.GrandparentRatingKey + "/" + strconv.Itoa(it.ParentIndex)
		}
	}
	if it.Year > 0 {
		l.Date = strconv.Itoa(it.Year)
	}
	sports := false
	for _, g := range it.Genre {
		if g.Tag != "" {
			l.Categories = append(l.Categories, g.Tag)
			sports = sports || g.Tag == "Sports"
		}
	}
	if len(it.Team) > 0 && !sports {
		l.Categories = append(l.Categories, "Sports")
	}
	if l.Rating != "" {
		l.RatingSystem = "MPAA"
		if strings.HasPrefix(l.Rating, "TV-") {
			l.RatingSystem = "VCHIP"
		}
	}
	return l
}

func datePart(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}
