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

package fileimport

import (
	"fmt"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
)

// The caps ImportState declares in api/download/v1alpha1/download_types.go.
// The apiserver rejects an apply that exceeds any of them WHOLE, not just the
// excess, so an uncapped status.import would be recorded nowhere: the
// delivery fails, redelivers, walks the same files and fails again until
// MaxDeliver. A full-series pack with more than 200 files is enough to do it.
const (
	// maxImportList is ImportState.Imported's and ImportState.Rejections'
	// +kubebuilder:validation:MaxItems.
	maxImportList = 200
	// maxRejectionChars is ImportState.Rejections' items:MaxLength. The
	// apiserver counts a string's length in characters, not bytes.
	maxRejectionChars = 1024
	// maxImportMessage is ImportState.Message's MaxLength.
	maxImportMessage = 2048
)

// capImported keeps the first maxImportList imported files. Every file past
// the cap still has its MediaFile, and the dedup fingerprint is computed
// from the full list before this is called, so only the status listing is
// shortened. The second result is how many were left out.
func capImported(imported []*downloadac.ImportedFileApplyConfiguration) ([]*downloadac.ImportedFileApplyConfiguration, int) {
	if len(imported) <= maxImportList {
		return imported, 0
	}
	return imported[:maxImportList], len(imported) - maxImportList
}

// capRejections fits rejections to status.import.rejections: each entry to
// maxRejectionChars, and the list to maxImportList, with the last slot
// saying how many more there were rather than dropping them silently.
func capRejections(rejections []string) []string {
	if len(rejections) == 0 {
		return nil
	}
	n := len(rejections)
	if n > maxImportList {
		n = maxImportList - 1
	}
	out := make([]string, 0, min(len(rejections), maxImportList))
	for _, r := range rejections[:n] {
		out = append(out, truncateChars(r, maxRejectionChars))
	}
	if n < len(rejections) {
		out = append(out, fmt.Sprintf("... and %d more rejections not listed", len(rejections)-n))
	}
	return out
}

// truncateChars cuts s to at most n characters (runes), never splitting one.
func truncateChars(s string, n int) string {
	if len(s) <= n { // at most n bytes is at most n runes
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
