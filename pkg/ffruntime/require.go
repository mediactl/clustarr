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

package ffruntime

import (
	"fmt"
	"strings"

	"github.com/obinnaokechukwu/ffgo/avcodec"
	"github.com/obinnaokechukwu/ffgo/avfilter"
	"github.com/obinnaokechukwu/ffgo/avformat"
)

// Needs names what a caller asks of FFmpeg, by FFmpeg's names.
type Needs struct{ Demuxers, Muxers, Decoders, Encoders, Filters []string }

// Require loads FFmpeg (Load) and fails naming every missing piece of n.
func Require(n Needs) error {
	if _, err := Load(); err != nil {
		return err
	}
	var missing []string
	for _, d := range n.Demuxers {
		if avformat.FindInputFormat(d) == nil {
			missing = append(missing, "demuxer "+d)
		}
	}
	for _, m := range n.Muxers {
		// A muxer is found by allocating an output context for it, freed at
		// once (spec §7.4: avformat.AllocOutputContext2).
		var oc avformat.FormatContext
		if err := avformat.AllocOutputContext2(&oc, nil, m, ""); err != nil || oc == nil {
			missing = append(missing, "muxer "+m)
			continue
		}
		avformat.FreeContext(oc)
	}
	for _, d := range n.Decoders {
		if avcodec.FindDecoderByName(d) == nil {
			missing = append(missing, "decoder "+d)
		}
	}
	for _, e := range n.Encoders {
		if avcodec.FindEncoderByName(e) == nil {
			missing = append(missing, "encoder "+e)
		}
	}
	for _, f := range n.Filters {
		if avfilter.GetByName(f) == nil {
			missing = append(missing, "filter "+f)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: this FFmpeg lacks %s", ErrUnavailable, strings.Join(missing, ", "))
	}
	return nil
}
