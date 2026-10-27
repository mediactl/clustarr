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

package importlist

import (
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	pkgimportlist "github.com/mediactl/clustarr/pkg/importlist"
)

// A series a list adds monitors every season but the specials (owner,
// 2026-10-27); it took the CRD's default, none, before.
func TestAListedSeriesMonitorsEverySeasonButTheSpecials(t *testing.T) {
	spec := seriesSpecFor("mal", pkgimportlist.Item{}, catalogv1alpha1.ListDefaults{QualityProfileRef: "hd", RootFolderRef: "tv"}, 81189)
	require.NotNil(t, spec.AddOptions)
	require.NotNil(t, spec.AddOptions.Monitor)
	require.Equal(t, catalogv1alpha1.SeriesMonitorUnmonitorSpecials, *spec.AddOptions.Monitor)
}
