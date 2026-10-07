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
	"errors"
	"fmt"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/ffruntime"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Needs names the demuxers the library's files use (spec §6.2).
var Needs = ffruntime.Needs{Demuxers: []string{"matroska", "mov", "mpegts", "avi", "flac", "mp3", "ogg", "wav"}}

// Prober probes in-process. New is the only way to get one: Disposition and
// ChannelLayout need the shim, so a Prober never exists without it.
type Prober struct{ _ [0]func() }

var _ mediainfo.Prober = (*Prober)(nil)

// New loads FFmpeg 9 and the shim (ffruntime.Load) and requires Needs.
func New() (*Prober, error) {
	if err := ffruntime.Require(Needs); err != nil {
		return nil, err
	}
	return &Prober{}, nil
}

// Probe reads path: the container, its streams and chapters, and the first
// frame of v:0. It runs under ffruntime.Do, and FFmpeg's I/O under the
// context (ffgo.WithInterrupt).
func (p *Prober) Probe(ctx context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
	ctx, span := tracing.Start(ctx, "mediainfo.native.Probe")
	defer span.End()
	var raw *mediainfo.Raw
	err := ffruntime.Do(ctx, func(ctx context.Context) (err error) { raw, err = probeRaw(ctx, path, true); return err })
	if err != nil {
		err = probeErr(path, err)
		tracing.RecordError(span, err)
		return nil, nil, err
	}
	return mediainfo.FromRaw(raw), raw, nil
}

// ProbeAudio reads path's first audio stream: the header only, no frame.
func (p *Prober) ProbeAudio(ctx context.Context, path string) (mediainfo.AudioProbe, error) {
	ctx, span := tracing.Start(ctx, "mediainfo.native.ProbeAudio")
	defer span.End()
	var ap mediainfo.AudioProbe
	err := ffruntime.Do(ctx, func(ctx context.Context) error {
		raw, err := probeRaw(ctx, path, false)
		if err == nil {
			ap, err = mediainfo.AudioProbeFromRaw(raw)
		}
		return err
	})
	if err != nil {
		err = probeErr(path, err)
		tracing.RecordError(span, err)
		return mediainfo.AudioProbe{}, err
	}
	return ap, nil
}

func probeErr(path string, err error) error {
	if errors.Is(err, ffruntime.ErrAbandoned) {
		return fmt.Errorf("mediainfo: probe %s: %w: %w", path, mediainfo.ErrProbeAbandoned, err)
	}
	return fmt.Errorf("mediainfo: probe %s: %w", path, err)
}
