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
// pkg/transcode/engine on ffgo, behind worker.Engine, probing through
// pkg/mediainfo/native. Only cmd/transcode imports it. The worker package
// never does: ffgo loads FFmpeg through purego, which makes the Go linker
// emit a dynamically linked binary, so the worker's interfaces stay free of
// it and the binary that supplies the engine chooses to link it.
package inprocess

import (
	"context"
	"errors"
	"fmt"

	"github.com/obinnaokechukwu/ffgo"

	"github.com/mediactl/clustarr/pkg/ffruntime"
	"github.com/mediactl/clustarr/pkg/mediainfo/native"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/engine"
	"github.com/mediactl/clustarr/pkg/transcode/selfcheck"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// Engine runs the standard's plans in-process; it probes through prober.
type Engine struct{ prober *native.Prober }

// New loads FFmpeg 9 and the shim, requires class's encoders, muxers and
// filters (the image's self-check list) and builds the in-process prober
// (spec §7.4). The error names what is missing; the worker then runs no
// ffgo task.
func New(class transcode.Hardware) (Engine, error) {
	if err := ffruntime.Require(needsFor(class)); err != nil {
		return Engine{}, err
	}
	p, err := native.New()
	if err != nil {
		return Engine{}, err
	}
	return Engine{prober: p}, nil
}

// needsFor is a pool class's self-check needs, as ffruntime.Needs.
func needsFor(class transcode.Hardware) ffruntime.Needs {
	c := selfcheck.ClassCPU
	switch class {
	case transcode.HardwareNVIDIA:
		c = selfcheck.ClassCUDA
	case transcode.HardwareIntel:
		c = selfcheck.ClassIntel
	}
	n := selfcheck.Needs[c]
	return ffruntime.Needs{Encoders: n.Encoders, Muxers: n.Muxers, Filters: n.Filters}
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
