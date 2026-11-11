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
const trackFileTemplate = "{Album Title}{ (Release Year)}/{Artist Name} - {Album Title} - {track:00}{ - Track Title}"

// multiDiscTrackFileTemplate is Lidarr's MultiDiscTrackFormat, the track
// preset of a release with two or more media (the 2026-11-10 non-video
// libraries design's D19): the album folder, then a folder per medium --
// "CD 02", or "02" alone for a medium whose format MusicBrainz does not
// name, which is why the format token carries its own trailing space
// rather than the template a literal one ("{Medium Format} {medium:00}"
// would leave a folder named " 02") -- then the one-disc preset's file.
// Without it every medium's track 1 renders the same name (All Eyez on
// Me's two discs collided).
const multiDiscTrackFileTemplate = "{Album Title}{ (Release Year)}/{Medium Format }{medium:00}/{Artist Name} - {Album Title} - {track:00}{ - Track Title}"

// TrackFile renders an album-relative track path (Lidarr convention).
func (e Engine) TrackFile(c Context) (string, error) {
	return e.Render(e.overrideOr(TokenTrackFile, trackFileTemplate), c)
}

// MultiDiscTrackFile renders an album-relative track path for a release of
// two or more media, with a folder per medium below the album folder
// (Lidarr's MultiDiscTrackFormat; TokenMultiDiscTrackFile overrides it).
// The caller decides from the release's media how many there are
// (catalogctx.TrackFilePath): the context alone cannot tell a one-disc
// release's medium 1 from a two-disc release's.
func (e Engine) MultiDiscTrackFile(c Context) (string, error) {
	return e.Render(e.overrideOr(TokenMultiDiscTrackFile, multiDiscTrackFileTemplate), c)
}
