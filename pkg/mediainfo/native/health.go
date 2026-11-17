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

package native

import (
	"context"
	"fmt"
	"time"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/obinnaokechukwu/ffgo/avutil"

	"github.com/mediactl/clustarr/pkg/ffruntime"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

var _ mediainfo.HealthProber = (*Prober)(nil)

// Health reads path end to end -- every packet, no frame decoded -- and
// reports the stretches of its timeline no audio or video stream has a
// packet in (mediainfo.Holes). Only Matroska and MP4 are judged: their
// timestamps run on from the file's start, where a transport stream's may
// jump by design. A read that fails part-way is the end of what the file
// holds, and the rest of its stated length is missing; one the context
// ended is an error, and nothing is known.
func (p *Prober) Health(ctx context.Context, path string) (mediainfo.Health, error) {
	ctx, span := tracing.Start(ctx, "mediainfo.native.Health")
	defer span.End()
	var h mediainfo.Health
	err := ffruntime.Do(ctx, func(ctx context.Context) (err error) { h, err = health(ctx, path); return err })
	if err != nil {
		err = probeErr(path, err)
		tracing.RecordError(span, err)
		return mediainfo.Health{}, err
	}
	return h, nil
}

func health(ctx context.Context, path string) (mediainfo.Health, error) {
	d, err := ffgo.NewDecoder(path, ffgo.WithInterrupt(ctx.Done()))
	if err != nil {
		if avutil.IsInvalidData(err) {
			return mediainfo.Health{}, fmt.Errorf("%w: %w", mediainfo.ErrNotMedia, err)
		}
		return mediainfo.Health{}, err
	}
	defer func() { _ = d.Close() }()
	track := Track(d)
	for {
		pkt, err := d.ReadPacket()
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return mediainfo.Health{}, cerr
			}
			break // the bytes end here, whatever the header says
		}
		if pkt == nil {
			break
		}
		Follow(track, pkt)
	}
	if err := ctx.Err(); err != nil {
		return mediainfo.Health{}, err
	}
	h := track.Health()
	h.Duration = max(time.Duration(d.DurationMicroseconds())*time.Microsecond, 0)
	return h, nil
}

// Track is a mediainfo.Tracker over d's audio and video streams, nil when
// d's container is not one that is judged.
func Track(d *ffgo.Decoder) *mediainfo.Tracker {
	t := mediainfo.NewTracker(d.FormatName(), time.Duration(d.DurationMicroseconds())*time.Microsecond, d.StartTime())
	for _, s := range d.Streams() {
		if s.Disposition&ffgo.DispositionAttachedPic != 0 {
			continue
		}
		switch s.Type {
		case ffgo.MediaTypeVideo:
			t.Stream(s.Index, true, s.TimeBase.Num, s.TimeBase.Den)
		case ffgo.MediaTypeAudio:
			t.Stream(s.Index, false, s.TimeBase.Num, s.TimeBase.Den)
		}
	}
	return t
}

// Follow records a packet d handed out on t: at its decode timestamp, else
// its presentation one; a packet with neither is passed over.
func Follow(t *mediainfo.Tracker, pkt *ffgo.Packet) {
	ts := pkt.DTS()
	if ts == avutil.AV_NOPTS_VALUE {
		ts = pkt.PTS()
	}
	if ts != avutil.AV_NOPTS_VALUE {
		t.Add(pkt.StreamIndex(), ts)
	}
}
