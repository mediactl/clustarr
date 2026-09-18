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

package musicbrainz

import "fmt"

// CoverArtURL builds a Cover Art Archive front-cover URL for a MusicBrainz
// release. size is one of 250, 500, 1200, or 0 for the full-size image;
// any other value is treated as 0. CAA has no API of its own, only this URL
// convention (docs/research/metadata.md §2.3: "no rate limiting rules at
// CAA"), so there is no client library and no HTTP call here -- CAA
// 307-redirects this to archive.org, and callers store the CAA URL, not the
// redirect target.
func CoverArtURL(releaseMBID string, size int) string {
	switch size {
	case 250, 500, 1200:
		return fmt.Sprintf("https://coverartarchive.org/release/%s/front-%d", releaseMBID, size)
	default:
		return fmt.Sprintf("https://coverartarchive.org/release/%s/front", releaseMBID)
	}
}
