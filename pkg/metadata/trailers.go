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

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/trailers"
)

// TrailerProvider lists the videos a provider names for a movie or a
// series -- TMDB's by the TMDB id, TheTVDB's for a series by its TVDB id --
// each with the site it is hosted at and its id there. It returns none
// with no error when ids name nothing it can look up or kind is not one it
// lists videos for. The trailers agent picks among them
// ([trailers.Pick]) for an item Plex's metadata service has no trailer for.
type TrailerProvider interface {
	Trailers(ctx context.Context, kind commonv1.MediaKind, ids ExternalIDs) ([]trailers.Video, error)
}
