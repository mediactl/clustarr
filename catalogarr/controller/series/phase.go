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

package series

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// Phase computes the coarse lifecycle state of a Series, mirroring §4.2's
// SeriesPhase enum and the EpisodesSynced condition.
func Phase(monitored, metadataReady, episodesSynced bool) catalogv1alpha1.SeriesPhase {
	switch {
	case !monitored:
		return catalogv1alpha1.SeriesPhaseUnmonitored
	case !metadataReady || !episodesSynced:
		return catalogv1alpha1.SeriesPhasePending
	default:
		return catalogv1alpha1.SeriesPhaseReady
	}
}
