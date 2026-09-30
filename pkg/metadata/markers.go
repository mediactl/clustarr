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

package metadata

import "context"

// Segment is one skip segment of a media file, in milliseconds from its
// start: an intro, a recap, the credits or a preview of the next episode.
type Segment struct {
	StartMs int64 `json:"startMs"`
	EndMs   int64 `json:"endMs"`
}

// Segments are a file's skip segments by kind, each list ordered by start.
type Segments struct {
	Intro   []Segment `json:"intro,omitempty"`
	Recap   []Segment `json:"recap,omitempty"`
	Credits []Segment `json:"credits,omitempty"`
	Preview []Segment `json:"preview,omitempty"`
}

// MarkersQuery names one file to a MarkersProvider: its title's ids, the
// season and episode for an episode (zero for a movie), and the file's
// duration, which identifies the release and places a segment that runs to
// the end.
type MarkersQuery struct {
	IDs        ExternalIDs
	Season     int32
	Episode    int32
	DurationMs int64
}

// MarkersProvider supplies a file's skip segments (TheIntroDB). It returns
// ErrNotFound when it has none for the title.
type MarkersProvider interface {
	Provider
	Markers(ctx context.Context, q MarkersQuery) (Segments, error)
}
