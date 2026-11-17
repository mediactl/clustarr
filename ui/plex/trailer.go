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
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mediactl/clustarr/pkg/trailers"
)

// An item's own trailer is answered to PMS as a clip whose part is a file:
// PMS takes a part's "file" from a provider's extra, reads the file itself
// and serves it as any local part (talos, 2026-11-17, PMS 1.43.5.11079: a
// test clip naming a film's own file came back as /library/parts/<id> with
// the file's real duration and size, HTTP 206). A part naming an outside
// address was stored too and did not play through the transcoder, and one
// naming a provider path answered 404, so the file is the only form.
//
// The clip's guid is the video's site and id, so the same video is the
// same clip across refreshes and a newer pick is a new one.

type trailerExtra struct {
	GUID                  string         `json:"guid"`
	Key                   string         `json:"key"`
	RatingKey             string         `json:"ratingKey"`
	Type                  string         `json:"type"`
	Subtype               string         `json:"subtype"`
	ExtraType             int            `json:"extraType"`
	Title                 string         `json:"title"`
	ParentTitle           string         `json:"parentTitle"`
	Duration              int64          `json:"duration"`
	AddedAt               int64          `json:"addedAt,omitempty"`
	OriginallyAvailableAt string         `json:"originallyAvailableAt,omitempty"`
	Year                  int            `json:"year,omitempty"`
	Thumb                 string         `json:"thumb,omitempty"`
	Media                 []trailerMedia `json:"Media"`
}

type trailerMedia struct {
	Container             string        `json:"container"`
	Duration              int64         `json:"duration"`
	Width                 int           `json:"width,omitempty"`
	Height                int           `json:"height,omitempty"`
	VideoCodec            string        `json:"videoCodec,omitempty"`
	AudioCodec            string        `json:"audioCodec,omitempty"`
	VideoResolution       string        `json:"videoResolution,omitempty"`
	OptimizedForStreaming bool          `json:"optimizedForStreaming"`
	Part                  []trailerPart `json:"Part"`
}

type trailerPart struct {
	File      string          `json:"file"`
	Container string          `json:"container"`
	Duration  int64           `json:"duration"`
	Size      int64           `json:"size,omitempty"`
	Stream    []trailerStream `json:"Stream"`
}

type trailerStream struct {
	ID         string `json:"id"`
	StreamType int    `json:"streamType"`
	Codec      string `json:"codec"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	Selected   bool   `json:"selected"`
}

// trailerClip is the clip a ready trailer record is answered as, for the
// item PMS asked about by ratingKey; false for a record that is not ready
// and for a file no mapping tells Plex the place of.
func trailerClip(e trailers.Entry, mappings []trailers.PathMapping, ratingKey string) (Extra, bool) {
	if e.State != trailers.StateReady || e.Video == nil || e.Path == "" {
		return nil, false
	}
	file, ok := trailers.PlexPath(mappings, e.Path)
	if !ok {
		return nil, false
	}
	id := strings.ToLower(e.Video.Site) + "-" + e.Video.Key
	title := e.Video.Name
	if title == "" {
		title = "Trailer"
	}
	container := strings.TrimPrefix(filepath.Ext(e.Path), ".")
	clip := trailerExtra{
		GUID:      "clustarr://trailer/" + strings.ToLower(e.Video.Site) + "/" + e.Video.Key,
		Key:       "/library/metadata/" + ratingKey + "/extras/" + id,
		RatingKey: id, Type: "clip", Subtype: "trailer", ExtraType: trailers.PlexTrailerType,
		Title: title, ParentTitle: "Trailer", Duration: e.DurationMillis,
		AddedAt: e.CheckedAt.Unix(),
		Media: []trailerMedia{{
			Container: container, Duration: e.DurationMillis, Width: e.Width, Height: e.Height,
			VideoCodec: e.VideoCodec, AudioCodec: e.AudioCodec, VideoResolution: videoResolution(e.Height),
			OptimizedForStreaming: true,
			Part: []trailerPart{{
				File: file, Container: container, Duration: e.DurationMillis, Size: e.SizeBytes,
				Stream: []trailerStream{
					{ID: id + "-video", StreamType: 1, Codec: e.VideoCodec, Width: e.Width, Height: e.Height, Selected: true},
					{ID: id + "-audio", StreamType: 2, Codec: e.AudioCodec, Selected: true},
				},
			}},
		}},
	}
	if !e.Video.PublishedAt.IsZero() {
		clip.OriginallyAvailableAt = e.Video.PublishedAt.UTC().Format("2006-01-02")
		clip.Year = e.Video.PublishedAt.UTC().Year()
	}
	if strings.EqualFold(e.Video.Site, "youtube") {
		// The site's own still of the video, at a public address PMS can
		// fetch.
		clip.Thumb = "https://i.ytimg.com/vi/" + e.Video.Key + "/hqdefault.jpg"
	}
	b, err := json.Marshal(clip)
	if err != nil {
		return nil, false
	}
	return b, true
}

// videoResolution is Plex's name for a picture's height.
func videoResolution(height int) string {
	switch {
	case height <= 0:
		return ""
	case height <= 576:
		return "sd"
	case height <= 720:
		return "720"
	case height <= 1080:
		return "1080"
	default:
		return strconv.Itoa(height)
	}
}
