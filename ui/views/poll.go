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

package views

// pollTrigger is every polled region's hx-trigger: it pauses while the
// tab is hidden and fires at once when it shows again (feedback.js raises
// shell:visible on the body). A region with more triggers appends them
// after a comma.
func pollTrigger(interval string) string {
	return "every " + interval + " [!document.hidden], shell:visible from:body"
}
