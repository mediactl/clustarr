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

// trackFileTemplate includes the album-folder segment: Lidarr's own
// convention is that the track format is album-relative, so TrackFile
// returns a path, not a bare filename.
const trackFileTemplate = "{Album Title} ({Release Year})/{Artist Name} - {Album Title} - {track:00} - {Track Title}"

// TrackFile renders an album-relative track path (Lidarr convention).
func (e Engine) TrackFile(c Context) (string, error) {
	return e.Render(e.overrideOr(TokenTrackFile, trackFileTemplate), c)
}
