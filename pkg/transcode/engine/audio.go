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
	"errors"
	"fmt"
	"io"

	"github.com/obinnaokechukwu/ffgo"

	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// maxAACRate is the highest sample rate the stage encodes AAC at; a 96 kHz
// source is resampled to it (Apple TV plays 48 kHz AAC).
const maxAACRate = 48000

// outputRate is the sample rate an output encodes at: AAC at the source's
// up to maxAACRate; AC-3 at the source's when AC-3 has it (32, 44.1 or 48
// kHz), else 48 kHz.
func outputRate(action string, source int) int {
	if action == standard.AudioAC3 {
		switch source {
		case 32000, 44100, 48000:
			return source
		}
		return 48000
	}
	if source <= 0 || source > maxAACRate {
		return maxAACRate
	}
	return source
}

// audioStage decodes one audio track once and encodes each of outs from
// it -- AC-3 5.1 and its AAC 2.0 companion, or one AAC track -- each
// through its own resampler into the output's layout by channel position
// (built from the first decoded frame's real layout, not the container's
// claim).
func audioStage(outs []standard.AudioPlan) stageFunc {
	return func(ctx context.Context, sc *stageContext) (err error) {
		sd, err := sc.dec.NewStreamDecoder(sc.src.Index, nil)
		if err != nil {
			return fmt.Errorf("decoder: %w", err)
		}
		defer func() { _ = sd.Close() }()
		encs := make([]*ffgo.AudioEncoder, len(outs))
		rates := make([]int, len(outs))
		defer func() {
			for _, e := range encs {
				if e != nil {
					_ = e.Close()
				}
			}
		}()
		srcs := make([]ffgo.EncodedStreamSource, len(outs))
		for k, a := range outs {
			name := "aac"
			if a.Action == standard.AudioAC3 {
				name = "ac3"
			}
			rates[k] = outputRate(a.Action, sc.src.SampleRate)
			if encs[k], err = ffgo.NewAudioEncoder(ffgo.AudioEncoderConfig2{
				EncoderName: name, SampleRate: rates[k], Layout: a.Layout, BitRate: a.BitRate,
				GlobalHeader: true, InputTimeBase: sd.TimeBase(),
			}); err != nil {
				return fmt.Errorf("%s encoder: %w", name, err)
			}
			srcs[k] = encs[k]
		}
		if err := sc.setup(srcs...); err != nil {
			return err
		}
		defer func() { sc.release(err) }() // before the encoders close

		res := make([]*ffgo.Resampler, len(outs))
		defer func() {
			for _, r := range res {
				if r != nil {
					_ = r.Close()
				}
			}
		}()
		encode := func(f ffgo.Frame) error {
			for k, a := range outs {
				if res[k] == nil {
					if res[k], err = ffgo.NewResampler(
						ffgo.AudioFormat{SampleRate: sc.src.SampleRate, Layout: f.ChannelLayout(), SampleFormat: ffgo.SampleFormat(f.Format())},
						ffgo.AudioFormat{SampleRate: rates[k], Layout: a.Layout, SampleFormat: ffgo.SampleFormatFLTP}); err != nil {
						return fmt.Errorf("resampler: %w", err)
					}
				}
				r, err := res[k].Resample(f)
				if err != nil {
					return fmt.Errorf("resample: %w", err)
				}
				if r.IsNil() {
					continue
				}
				r.SetPTS(f.PTS()) // the resampler's frames carry none; the encoder anchors on the first
				err = encs[k].Encode(r, sc.emits[k])
				_ = r.Free()
				if err != nil {
					return err
				}
			}
			return nil
		}
		drain := func() error {
			for {
				f, err := sd.Receive()
				if errors.Is(err, ffgo.ErrAgain) || errors.Is(err, io.EOF) {
					return nil
				}
				if err != nil {
					return fmt.Errorf("decode: %w", err)
				}
				if err := encode(f); err != nil {
					return err
				}
			}
		}
		if err := decodeAll(ctx, sc.in, sd, drain); err != nil {
			return err
		}
		for k := range outs {
			if res[k] != nil {
				if tail, err := res[k].Flush(); err == nil && !tail.IsNil() {
					err := encs[k].Encode(tail, sc.emits[k])
					_ = tail.Free()
					if err != nil {
						return err
					}
				}
			}
			if err := encs[k].Flush(sc.emits[k]); err != nil {
				return err
			}
		}
		return nil
	}
}

// decodeAll feeds every packet of in to sd, calling drain after each and
// once more after end of stream; a full decoder (ErrAgain) is drained and
// sent the same packet again.
func decodeAll(ctx context.Context, in <-chan *ffgo.Packet, sd *ffgo.StreamDecoder, drain func() error) error {
	send := func(p *ffgo.Packet) error {
		for {
			err := sd.Send(p)
			if !errors.Is(err, ffgo.ErrAgain) {
				return err
			}
			if err := drain(); err != nil {
				return err
			}
		}
	}
	for {
		select {
		case p, ok := <-in:
			if !ok {
				if err := send(nil); err != nil {
					return fmt.Errorf("decode: %w", err)
				}
				return drain()
			}
			err := send(p)
			_ = p.Free()
			if err != nil {
				return fmt.Errorf("decode: %w", err)
			}
			if err := drain(); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
