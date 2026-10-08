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

// Package testmedia writes deterministic test clips on ffgo, the model of an
// ffmpeg command line, so no test runs the ffmpeg or ffprobe CLI (owner
// decision of 2026-10-06; upgrade guide U2). Tests, hack/record-goldens and
// test/fixtures/mkclips import it; no binary does (TestNoBinaryLinksTestmedia).
//
// A Spec is an ffmpeg command line: lavfi graphs as sources, an encoder and
// its options per stream, HDR10 side data, text subtitles, attachments,
// chapters and tags. Write makes the clip; Inspect, Frames, Packets and
// ChannelRMS read one back in place of ffprobe and ffmpeg's astats; Remux is
// `ffmpeg -i src -map 0 -c copy -metadata ...`.
package testmedia
