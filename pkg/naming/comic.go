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

package naming

// issueFileTemplate includes the issue segment under the comic-series
// folder, matching the Kavita/Komga convention of one folder per series.
const issueFileTemplate = "{Comic Series Title}/{Comic Series Title} c{issue}"

// IssueFile renders the comic issue file path.
func (e Engine) IssueFile(c Context) (string, error) {
	return e.Render(e.overrideOr(TokenIssueFile, issueFileTemplate), c)
}
