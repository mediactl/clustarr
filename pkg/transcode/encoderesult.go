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

package transcode

// EncodeResult is what an engine's run reports besides its error: the tail
// of FFmpeg's log on a failure, and the end of the source as the run
// demuxed it.
type EncodeResult struct {
	// LogTail is the end of FFmpeg's log, on a failure.
	LogTail string
	// SourceEndMillis is where the source's video and the audio the plan
	// keeps end -- the last demuxed packet's timestamp plus its duration, in
	// the output's timeline -- or 0 when the run did not measure it. It is
	// the length Verify holds the output to: the source's container duration
	// may be a dropped track's or a subtitle's (RoboCop's Turkish DTS 3.4 s
	// past the video, The Mitchells vs. the Machines' SubRip cues 265 s past
	// it, 2026-10-23 and -25), which no output can match.
	SourceEndMillis int64
}
