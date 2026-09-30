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

package plex

import "testing"

// TestCleanRelativeRefusesWhatCannotBeAFileUnderTheLibrary is rule 0's
// guard on its own: the handler tests cannot pin it, because such a name is
// never a suffix of a stored path anyway.
func TestCleanRelativeRefusesWhatCannotBeAFileUnderTheLibrary(t *testing.T) {
	for in, want := range map[string]string{
		"Heat (1995)/Heat (1995).mkv":      "Heat (1995)/Heat (1995).mkv",
		"movies/./Heat (1995)/../Heat.mkv": "movies/Heat.mkv",
		"":                                 "",
		".":                                "",
		"..":                               "",
		"../../etc/passwd":                 "",
		"/data/media/movies/Heat.mkv":      "",
		"a/../../b.mkv":                    "",
	} {
		got, ok := cleanRelative(in)
		if want == "" {
			if ok {
				t.Errorf("cleanRelative(%q) = %q, want refused", in, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("cleanRelative(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}
