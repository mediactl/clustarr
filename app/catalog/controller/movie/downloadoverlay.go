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

package movie

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
)

// DownloadOverlay maps rollup.EntryOverlay's phase-independent verdict
// onto MoviePhase. The decision logic (which entry phases mean what) lives
// once, in app/catalog/controller/rollup -- this is only the type
// translation. e is the Movie's active grab entry (rollup.ActiveEntry), nil
// for none (ADR-0019 §6.11).
func DownloadOverlay(e *catalogv1alpha1.DownloadEntry) (phase catalogv1alpha1.MoviePhase, active bool) {
	overlay, active := rollup.EntryOverlay(e)
	switch overlay {
	case rollup.OverlayDownloading:
		return catalogv1alpha1.MoviePhaseDownloading, active
	default:
		return "", active
	}
}
