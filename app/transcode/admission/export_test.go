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

package admission

import (
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
)

// InFlight is the files of class whose block holds a slot (Queued or
// Running), whatever their pool: the envtests' count of what a rebuild
// read, before any pass has told the ledger its profiles.
func (l *Ledger) InFlight(class transcodev1alpha1.Hardware) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.files {
		if slotPhase(e.view.Phase) && e.view.Class == class {
			n++
		}
	}
	return n
}
