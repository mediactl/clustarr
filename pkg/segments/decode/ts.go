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

package decode

import (
	"math"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/obinnaokechukwu/ffgo/avutil"
)

// audioTS gives each decoded audio frame the pts ffmpeg(1) filters it at
// (fftools/ffmpeg_dec.c audio_ts_process and audio_samplerate_update).
type audioTS struct {
	lastTB     ffgo.Rational // last_frame_tb
	lastPTS    int64         // last_frame_pts
	lastDurEst int64         // last_frame_duration_est
	lastRate   int           // last_frame_sample_rate
	delta      int64         // last_filter_in_rescale_delta
}

func newAudioTS() audioTS {
	return audioTS{lastTB: ffgo.NewRational(1, 1), lastPTS: avutil.AV_NOPTS_VALUE, delta: avutil.AV_NOPTS_VALUE}
}

func (s *audioTS) update(frameTB ffgo.Rational, rate int) ffgo.Rational {
	if rate == s.lastRate {
		return s.lastTB
	}
	prev := int64(s.lastTB.Den)
	g := gcd(prev, int64(rate))
	tb := ffgo.NewRational(1, 28224000) // LCM of 192000 and 44100, when exact is impossible
	if prev/g < math.MaxInt32/int64(rate) {
		tb = ffgo.NewRational(1, int32(prev/g*int64(rate)))
	}
	if frameTB.Num == 1 && frameTB.Den > tb.Den && frameTB.Den%tb.Den == 0 {
		tb = frameTB
	}
	if s.lastPTS != avutil.AV_NOPTS_VALUE {
		s.lastPTS = ffgo.RescaleQ(s.lastPTS, s.lastTB, tb)
	}
	s.lastDurEst = ffgo.RescaleQ(s.lastDurEst, s.lastTB, tb)
	s.lastTB, s.lastRate = tb, rate
	return tb
}

// next is pts (in frameTB, or AV_NOPTS_VALUE) as ffmpeg(1) gives the frame
// to its filter graph, in 1/rate.
func (s *audioTS) next(pts int64, frameTB ffgo.Rational, rate, nbSamples int) int64 {
	tb := s.update(frameTB, rate)
	pred := int64(0)
	if s.lastPTS != avutil.AV_NOPTS_VALUE {
		pred = s.lastPTS + s.lastDurEst
	}
	if pts == avutil.AV_NOPTS_VALUE {
		pts, frameTB = pred, tb
	} else if s.lastPTS != avutil.AV_NOPTS_VALUE && pts > ffgo.RescaleQRnd(pred, tb, frameTB, ffgo.RoundUp) {
		s.delta = avutil.AV_NOPTS_VALUE // a gap: reset the conversion state
	}
	pts = ffgo.RescaleDelta(frameTB, pts, tb, nbSamples, &s.delta, tb)
	s.lastPTS = pts
	s.lastDurEst = ffgo.RescaleQ(int64(nbSamples), ffgo.NewRational(1, int32(rate)), tb)
	return ffgo.RescaleQ(pts, tb, ffgo.NewRational(1, int32(rate)))
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
