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

package audiobook

import (
	"k8s.io/utils/ptr"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// ApplyAddOptions records spec.addOptions as acted on (2026-11-10 design
// D11; AudiobookStatus.addOptionsApplied): true from the first pass that
// reaches the decision point, and sent on every pass after that, since
// server-side apply releases a field a manager stops sending -- the Movie's
// and Series' rule for the same flag (movie.Reconciler's reconcileNormal).
// The reconciler calls it once per pass where it renders its complete set,
// and reassertKnownStatus re-sends the stored flag on the early returns.
//
// The one option, searchForMissing (default true), asks for the audiobook
// to be searched on add. It is read as the Movie and the Series read
// theirs, which is as the search planner does: a monitored audiobook never
// searched gets its one live search once its metadata is in, so an added
// audiobook is searched without this package asking for it, and the search
// stage reads a false through SearchOnAdd (app/remediation/search's
// addedUnsearched, searchplan.View.NoSearchOnAdd): the never-searched
// audiobook's first automatic search then asks the release index alone.
func ApplyAddOptions(statusAC *catalogac.AudiobookStatusApplyConfiguration, _ *catalogv1alpha1.Audiobook) *catalogac.AudiobookStatusApplyConfiguration {
	return statusAC.WithAddOptionsApplied(true)
}

// SearchOnAdd reports whether m was added with a search wanted:
// searchForMissing not turned off. It is the search stage's reading of the
// flag, so it says nothing of addOptionsApplied: the flag governs the
// audiobook's first automatic search, whenever that is due, not the pass
// that records the options as applied.
func SearchOnAdd(m *catalogv1alpha1.Audiobook) bool {
	return ptr.Deref(m.Spec.AddOptions.SearchForMissing, true)
}
