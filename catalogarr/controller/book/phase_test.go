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

package book_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/book"
)

func TestPhase(t *testing.T) {
	tests := []struct {
		name                                       string
		monitored, hasFile, cutoffMet, pendingGrab bool
		want                                       catalogv1alpha1.BookPhase
	}{
		{"unmonitored outranks everything", false, true, true, true, catalogv1alpha1.BookPhaseUnmonitored},
		{"file and cutoff met is imported", true, true, true, false, catalogv1alpha1.BookPhaseImported},
		{"pending grab outranks wanted", true, false, false, true, catalogv1alpha1.BookPhaseDelayed},
		{"pending grab outranks cutoff unmet", true, true, false, true, catalogv1alpha1.BookPhaseDelayed},
		{"file without cutoff met is cutoff unmet", true, true, false, false, catalogv1alpha1.BookPhaseCutoffUnmet},
		{"no file, no pending grab is wanted", true, false, false, false, catalogv1alpha1.BookPhaseWanted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := book.Phase(tt.monitored, tt.hasFile, tt.cutoffMet, tt.pendingGrab)
			assert.Equal(t, tt.want, got)
		})
	}
}
