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

// maxAACRate is the highest sample rate the stage encodes at; a 96 kHz
// source is resampled to it (Apple TV plays 48 kHz AAC).
const maxAACRate = 48000

// audioStage decodes one audio track, resamples it into the plan's layout
// by channel position (the resampler is built from the first decoded
// frame's real layout, not the container's claim), and encodes AAC.
func audioStage(a standard.AudioPlan) stageFunc {
	return func(ctx context.Context, sc *stageContext) error {
		sd, err := sc.dec.NewStreamDecoder(sc.src.Index, nil)
		if err != nil {
			return fmt.Errorf("decoder: %w", err)
		}
		defer func() { _ = sd.Close() }()
		rate := sc.src.SampleRate
		if rate <= 0 || rate > maxAACRate {
			rate = maxAACRate
		}
		enc, err := ffgo.NewAudioEncoder(ffgo.AudioEncoderConfig2{
			SampleRate: rate, Layout: a.Layout, BitRate: a.BitRate,
			GlobalHeader: true, InputTimeBase: sd.TimeBase(),
		})
		if err != nil {
			return fmt.Errorf("aac encoder: %w", err)
		}
		defer func() { _ = enc.Close() }()
		if err := sc.setup(enc); err != nil {
			return err
		}

		var res *ffgo.Resampler
		defer func() {
			if res != nil {
				_ = res.Close()
			}
		}()
		encode := func(f ffgo.Frame) error {
			if res == nil {
				if res, err = ffgo.NewResampler(
					ffgo.AudioFormat{SampleRate: sc.src.SampleRate, Layout: f.ChannelLayout(), SampleFormat: ffgo.SampleFormat(f.Format())},
					ffgo.AudioFormat{SampleRate: rate, Layout: a.Layout, SampleFormat: ffgo.SampleFormatFLTP}); err != nil {
					return fmt.Errorf("resampler: %w", err)
				}
			}
			r, err := res.Resample(f)
			if err != nil {
				return fmt.Errorf("resample: %w", err)
			}
			if r.IsNil() {
				return nil
			}
			defer func() { _ = r.Free() }()
			r.SetPTS(f.PTS()) // the resampler's frames carry none; the encoder anchors on the first
			return enc.Encode(r, sc.emit)
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
		if res != nil {
			if tail, err := res.Flush(); err == nil && !tail.IsNil() {
				err := enc.Encode(tail, sc.emit)
				_ = tail.Free()
				if err != nil {
					return err
				}
			}
		}
		return enc.Flush(sc.emit)
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
