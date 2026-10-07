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

// Package inprocess is the squasharr worker's in-process engine:
// pkg/transcode/engine on ffgo, behind worker.Engine. Only
// cmd/transcode imports it. The worker package never does, because
// cmd/clustarr imports the worker package for planning, and ffgo loads
// FFmpeg through purego, which makes the Go linker emit a dynamically
// linked binary that the distroless controller image cannot start.
package inprocess

import (
	"context"
	"errors"
	"fmt"

	"github.com/obinnaokechukwu/ffgo"

	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/engine"
	"github.com/mediactl/clustarr/pkg/transcode/selfcheck"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// Engine runs the standard's plans in-process.
type Engine struct{}

// New loads FFmpeg and checks it can run the engine: FFmpeg 9 with an ffgo
// shim built for it, which the transcoder image carries. The
// error names what is missing; the worker then runs no ffgo task.
func New() (Engine, error) {
	if err := ffgo.Init(); err != nil {
		return Engine{}, fmt.Errorf("load FFmpeg: %w", err)
	}
	if _, avc, _ := ffgo.Version(); avc>>16 != 63 {
		return Engine{}, fmt.Errorf("libavcodec %d is not FFmpeg 9's (63)", avc>>16)
	}
	if d := ffgo.Diagnose(); !d.ShimLoaded {
		return Engine{}, fmt.Errorf("no ffgo shim for FFmpeg 9 loaded: %s", d.ShimError)
	}
	return Engine{}, nil
}

// Encode runs plan from input to output on tier's device, calling progress
// from the muxer. A failure returns FFmpeg's log tail with it; a device
// that cannot be opened is transcode.ErrDeviceUnavailable.
func (Engine) Encode(ctx context.Context, plan standard.Result, tier transcode.Tier, input, output string,
	progress func(transcode.Progress),
) (string, error) {
	dev, err := openDevice(tier)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", transcode.ErrDeviceUnavailable, tier, err)
	}
	if dev != nil {
		defer func() { _ = dev.Close() }()
	}
	_, err = engine.Run(ctx, plan, input, output, engine.Options{HWDevice: dev, Progress: progress})
	if err == nil {
		return "", nil
	}
	var ee *engine.Error
	if errors.As(err, &ee) {
		return ee.LogTail, err
	}
	return "", err
}

// Verify probes output against exp and source through ffgo.
func (Engine) Verify(ctx context.Context, source, output string, exp standard.Expectation) (*transcode.Report, error) {
	return engine.Verify(ctx, source, output, exp)
}

// openDevice opens the GPU a tier's plan decodes, filters or encodes on;
// nil for the CPU tier. A variable so a test can make a device fail.
var openDevice = func(tier transcode.Tier) (*ffgo.HWDevice, error) {
	switch tier {
	case transcode.TierNVENC:
		return ffgo.NewHWDevice(ffgo.HWDeviceTypeCUDA, "")
	case transcode.TierQSV:
		return ffgo.NewHWDevice(ffgo.HWDeviceTypeQSV, "")
	case transcode.TierVAAPI:
		node := selfcheck.IntelRenderNode()
		if node == "" {
			return nil, errors.New("no Intel render node in this pod")
		}
		return ffgo.NewHWDevice(ffgo.HWDeviceTypeVAAPI, node)
	}
	return nil, nil
}
