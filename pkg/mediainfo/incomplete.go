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

package mediainfo

import (
	"errors"
	"fmt"

	ffprobe "gopkg.in/vansante/go-ffprobe.v2"
)

// ErrIncompleteProbe is a probe whose first video frame could not be read
// although the stream says the video may be HDR ([IncompleteHDR]): its HDR
// format is unknown, and reading it as SDR would plan an SDR encode of an
// HDR source. The cause may be passing (a timeout, a mount that blinked),
// so it is an error the caller retries, not a verdict.
var ErrIncompleteProbe = errors.New("mediainfo: the first video frame could not be read, so the HDR format is unknown")

// IncompleteHDR is the rule both probes apply when the first video frame
// could not be read (raw.FrameErr): an error wrapping ErrIncompleteProbe
// and the frame's own failure when the primary video stream says it may be
// HDR, else nil.
//
// The stream says so through what the demuxer read without decoding: a PQ
// or HLG transfer, BT.2020 primaries or matrix, or HDR10's mastering
// display or content light level as stream side data. ClassifyHDR reads
// the frame's tags, so without the frame such a file would classify as
// SDR (HdrFormatNone). A stream that says none of this -- tagged BT.709,
// or untagged, as most SDR files are -- classifies as SDR either way, and
// is not refused: a file whose first frame does not decode (a damaged
// start, a codec this FFmpeg cannot decode) would otherwise never be
// probed at all, and the library would lose a file over a colour detail
// that is SDR's on every other evidence. The transcode worker, which writes
// a final file and has no stream-level colour tags to read (ffgo exposes
// none), refuses every incomplete probe instead (app/squash/worker).
func IncompleteHDR(raw *Raw) error {
	if raw == nil || raw.FrameErr == nil {
		return nil
	}
	v := primaryVideoStream(raw.Streams)
	if v == nil {
		return nil
	}
	if why := streamSaysHDR(v); why != "" {
		return fmt.Errorf("%w: the stream says %s: %w", ErrIncompleteProbe, why, raw.FrameErr)
	}
	return nil
}

// streamSaysHDR names the stream-level evidence that v may be HDR, "" when
// there is none.
func streamSaysHDR(v *ffprobe.Stream) string {
	switch {
	case v.ColorTransfer == "smpte2084" || v.ColorTransfer == "arib-std-b67":
		return "transfer " + v.ColorTransfer
	case v.ColorPrimaries == "bt2020":
		return "primaries bt2020"
	case v.ColorSpace == "bt2020nc" || v.ColorSpace == "bt2020c":
		return "matrix " + v.ColorSpace
	}
	for _, sd := range v.SideDataList {
		switch sd.Type {
		case ffprobe.SideDataTypeMasteringDisplayMetadata, ffprobe.SideDataTypeContentLightLevel:
			return "side data " + sd.Type
		}
	}
	return ""
}

// primaryVideoStream is the first video stream that is not cover art: the
// one whose first frame the probes read.
func primaryVideoStream(streams []*ffprobe.Stream) *ffprobe.Stream {
	for _, s := range streams {
		if s != nil && s.CodecType == string(ffprobe.StreamVideo) && s.Disposition.AttachedPic == 0 {
			return s
		}
	}
	return nil
}
