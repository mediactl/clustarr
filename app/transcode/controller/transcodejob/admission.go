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

package transcodejob

import "github.com/mediactl/clustarr/app/transcode/admission"

// The slot scheduler moved to app/transcode/admission (F6.3); this doomed
// controller reads it through these names until F6.6 deletes it.
type (
	// Slot is admission.Slot.
	Slot = admission.Slot
	// Budget is admission.Budget.
	Budget = admission.Budget
)

// Admit is admission.Admit.
func Admit(queued, running []Slot, budget Budget) []Slot {
	return admission.Admit(queued, running, budget)
}

// admitsBefore is admission.AdmitsBefore.
func admitsBefore(a, b Slot) bool { return admission.AdmitsBefore(a, b) }
