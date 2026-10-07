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

package v1alpha1

import "path/filepath"

// PathBeside is the sidecar's absolute path: Name in the directory of the
// media file at mediaPath, where the subtitles planner found it (loop spec
// §2.7). It reports false for a name that is not one plain file name --
// empty, "." or "..", or holding a separator -- so a caller that moves or
// deletes what it returns never leaves the media file's folder.
func (s Sidecar) PathBeside(mediaPath string) (string, bool) {
	if s.Name == "" || s.Name == "." || s.Name == ".." || filepath.Base(s.Name) != s.Name {
		return "", false
	}
	return filepath.Join(filepath.Dir(mediaPath), s.Name), true
}
