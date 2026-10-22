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
}

// ImageSetImage is one image of a chosen set. Season and Episode are 0 for
// the item's own poster or background; an episode's thumbnail names both.
type ImageSetImage struct {
	// Type is the catalog image type: poster, fanart or screenshot.
	Type    string
	Season  int32
	Episode int32
	URL     string
}

// ImageSet is a provider's choice for an item.
type ImageSet struct {
	Provider  string
	SetID     string
	Title     string
	Creator   string
	URL       string
	UpdatedAt time.Time
	// Reason says why this set was chosen over the others, for the log.
	Reason string
	Images []ImageSetImage
}

// ImageSetProvider chooses an item's image set (MediUX). It returns
// ErrNotFound when it has no set for the item.
type ImageSetProvider interface {
	Provider
	ImageSet(ctx context.Context, q ImageSetQuery) (*ImageSet, error)
}
