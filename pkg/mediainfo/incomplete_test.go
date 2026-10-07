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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ffprobe "gopkg.in/vansante/go-ffprobe.v2"
)

// IncompleteHDR is the rule both probes share: a first frame that could
// not be read leaves the HDR format unknown, not SDR, whenever the stream
// itself says the video may be HDR -- its transfer is PQ or HLG, its
// primaries or matrix BT.2020, or it carries HDR10's static metadata.
func TestIncompleteHDRWhenTheStreamSaysTheVideoMayBeHDR(t *testing.T) {
	frameErr := errors.New("ffprobe frame probe: exit status 1")
	video := func(s ffprobe.Stream) *ffprobe.Stream {
		s.CodecType = string(ffprobe.StreamVideo)
		return &s
	}
	cover := &ffprobe.Stream{
		CodecType: string(ffprobe.StreamVideo), CodecName: "mjpeg",
		Disposition: ffprobe.StreamDisposition{AttachedPic: 1},
	}
	for name, c := range map[string]struct {
		streams  []*ffprobe.Stream
		frameErr error
		want     bool
	}{
		"PQ transfer":         {[]*ffprobe.Stream{video(ffprobe.Stream{ColorTransfer: "smpte2084"})}, frameErr, true},
		"HLG transfer":        {[]*ffprobe.Stream{video(ffprobe.Stream{ColorTransfer: "arib-std-b67"})}, frameErr, true},
		"BT.2020 primaries":   {[]*ffprobe.Stream{video(ffprobe.Stream{ColorPrimaries: "bt2020"})}, frameErr, true},
		"BT.2020 matrix":      {[]*ffprobe.Stream{video(ffprobe.Stream{ColorSpace: "bt2020nc"})}, frameErr, true},
		"mastering display":   {[]*ffprobe.Stream{video(ffprobe.Stream{SideDataList: ffprobe.SideDataList{{Type: ffprobe.SideDataTypeMasteringDisplayMetadata}}})}, frameErr, true},
		"content light level": {[]*ffprobe.Stream{video(ffprobe.Stream{SideDataList: ffprobe.SideDataList{{Type: ffprobe.SideDataTypeContentLightLevel}}})}, frameErr, true},
		"after cover art":     {[]*ffprobe.Stream{cover, video(ffprobe.Stream{ColorTransfer: "smpte2084"})}, frameErr, true},
		"BT.709":              {[]*ffprobe.Stream{video(ffprobe.Stream{ColorTransfer: "bt709", ColorPrimaries: "bt709", ColorSpace: "bt709"})}, frameErr, false},
		"untagged":            {[]*ffprobe.Stream{video(ffprobe.Stream{})}, frameErr, false},
		"no video":            {[]*ffprobe.Stream{{CodecType: string(ffprobe.StreamAudio)}}, frameErr, false},
		"the frame was read":  {[]*ffprobe.Stream{video(ffprobe.Stream{ColorTransfer: "smpte2084"})}, nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			err := IncompleteHDR(&Raw{Streams: c.streams, FrameErr: c.frameErr})
			if !c.want {
				assert.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrIncompleteProbe)
			assert.ErrorIs(t, err, frameErr, "the frame's own failure is kept")
		})
	}
	assert.NoError(t, IncompleteHDR(nil))
}
