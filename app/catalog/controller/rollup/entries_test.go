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

package rollup

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// TestEntryNonTerminalAfterImport: a torrent entry stays Seeding after its
// import (app/grab/lifecycle's afterImport), and an imported entry is no
// longer the owner's active download -- before the smoke test (2026-10-07)
// a transcoded movie read Downloading for its torrent's whole seeding period.
func TestEntryNonTerminalAfterImport(t *testing.T) {
	entry := func(phase commonv1.DownloadPhase, imp catalogv1alpha1.ImportPhase) *catalogv1alpha1.DownloadEntry {
		e := &catalogv1alpha1.DownloadEntry{ID: "e", Phase: phase}
		if imp != "" {
			e.Import = &catalogv1alpha1.DownloadImportSummary{Phase: imp}
		}
		return e
	}
	for _, tc := range []struct {
		name string
		e    *catalogv1alpha1.DownloadEntry
		want bool
	}{
		{"downloading", entry(commonv1.DownloadPhaseDownloading, ""), true},
		{"completed, import pending", entry(commonv1.DownloadPhaseCompleted, catalogv1alpha1.ImportPhasePending), true},
		{"seeding before its import", entry(commonv1.DownloadPhaseSeeding, ""), true},
		{"seeding after its import", entry(commonv1.DownloadPhaseSeeding, catalogv1alpha1.ImportPhaseImported), false},
		{"held import", entry(commonv1.DownloadPhaseSeeding, catalogv1alpha1.ImportPhaseHeld), false},
		{"imported", entry(commonv1.DownloadPhaseImported, catalogv1alpha1.ImportPhaseImported), false},
		{"blocklisted", entry(commonv1.DownloadPhaseBlocklisted, ""), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, EntryNonTerminal(tc.e))
			_, active := EntryOverlay(tc.e)
			assert.Equal(t, tc.want, active)
		})
	}
	assert.Nil(t, ActiveEntry([]catalogv1alpha1.DownloadEntry{*entry(commonv1.DownloadPhaseSeeding, catalogv1alpha1.ImportPhaseImported)}, nil),
		"an imported, seeding entry is no activeDownloadRef")
}
