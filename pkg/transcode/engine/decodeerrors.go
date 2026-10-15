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

package engine

import (
	"context"
	"fmt"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/obinnaokechukwu/ffgo/avutil"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// tolerantDecoder decodes a damaged stream as FFmpeg's own decode does
// (fftools/ffmpeg_dec.c): a packet the decoder refuses as invalid data, or
// a frame that fails to decode, is counted and skipped rather than failing
// the run, and the stream fails only past FFmpeg's max_error_rate -- more
// than 2/3 of everything it decoded or refused. One FLAC frame of 63 Up
// would not decode ("invalid residual", 2026-10-15), and that packet failed
// every attempt. An audio stage loses no time to a skipped packet: ffgo's
// AudioEncoder fills a gap in its input's timestamps with silence, so the
// audio keeps its length and its sync. Any other error still fails the
// stage.
type tolerantDecoder struct {
	*ffgo.StreamDecoder
	skipped, decoded int64
}

func (d *tolerantDecoder) Send(p *ffgo.Packet) error {
	err := d.StreamDecoder.Send(p)
	if p != nil && avutil.IsInvalidData(err) {
		d.skipped++
		return nil
	}
	return err
}

// Receive reads a frame that failed to decode as no frame yet, so the next
// packet is sent, as FFmpeg's decode loop moves on.
func (d *tolerantDecoder) Receive() (ffgo.Frame, error) {
	f, err := d.StreamDecoder.Receive()
	switch {
	case err == nil:
		d.decoded++
	case avutil.IsInvalidData(err):
		d.skipped++
		return f, ffgo.ErrAgain
	}
	return f, err
}

// finish fails a stream past FFmpeg's error rate, and logs one it
// tolerated.
func (d *tolerantDecoder) finish(ctx context.Context) error {
	if d.skipped == 0 {
		return nil
	}
	total := d.skipped + d.decoded
	if d.skipped*3 > total*2 {
		return fmt.Errorf("decode: %d of %d packets and frames would not decode, past FFmpeg's limit of 2/3", d.skipped, total)
	}
	logging.FromContext(ctx).WarnContext(ctx, "engine: skipped packets that would not decode (a damaged source)",
		"skipped", d.skipped, "decoded", d.decoded)
	return nil
}
