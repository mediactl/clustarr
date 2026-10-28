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

import (
	"context"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// ImageSetQuery names an item to an image-set provider: a movie or series
// by its TMDB id, a series alternatively by its TheTVDB id (the provider
// maps it), with what the series has so the provider can judge a set's
// completeness.
type ImageSetQuery struct {
	Kind   commonv1.MediaKind
	TMDBID string
	TVDBID string
	// Seasons are the series' season numbers, specials left out.
	Seasons []int32
	// Episodes is, by season, how many episodes the series has.
	Episodes map[int32]int32
	// SetID, when set, names the set to anchor on instead of choosing one;
	// the item's other sets still fill what it lacks.
	SetID string
}

// Image scopes: what an ImageSetImage is an image of.
const (
	ImageScopeItem    = ""        // the item's own poster or background
	ImageScopeSeason  = "season"  // a season's poster, specials (0) included
	ImageScopeEpisode = "episode" // an episode's thumbnail
)

// ImageSetImage is one image of an item's composed sets: the item's own,
// a season's (Season) or an episode's (Season and Episode), by Scope --
// a specials poster is season 0's, never the item's.
type ImageSetImage struct {
	// Type is the catalog image type: poster, fanart or screenshot.
	Type    string
	Scope   string
	Season  int32
	Episode int32
	URL     string
	// SetID is the set the image came from.
	SetID string
}

// ImageSetSource is a set an item's images came from.
type ImageSetSource struct {
	SetID   string
	Title   string
	Creator string
	// Kind is what the set was made for: show, movie or collection.
	Kind   string
	URL    string
	Images int
}

// ImageSet is a provider's images for an item, composed from its sets
// (the design's "Composed sets"): SetID, Title, Creator and URL name the
// anchor, the set the look is built around, and Sets every set that
// supplied an image, the anchor first.
type ImageSet struct {
	Provider  string
	SetID     string
	Title     string
	Creator   string
	URL       string
	UpdatedAt time.Time
	// Reason says why the anchor was chosen and what filled the rest, for
	// the log.
	Reason string
	Images []ImageSetImage
	Sets   []ImageSetSource
}

// ImageSetProvider composes an item's images from its image sets
// (MediUX). It returns ErrNotFound when it has no set for the item.
type ImageSetProvider interface {
	Provider
	ImageSet(ctx context.Context, q ImageSetQuery) (*ImageSet, error)
}
