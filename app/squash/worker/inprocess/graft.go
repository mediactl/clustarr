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

package inprocess

import (
	"context"
	"errors"
	"fmt"

	"github.com/mediactl/clustarr/app/squash/grafttask"
	"github.com/mediactl/clustarr/app/squash/worker/graft"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/engine"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// prepared is a graft.Prepared as grafttask.Prepared.
type prepared struct{ p *graft.Prepared }

func (p prepared) GraftTag() string          { return p.p.GraftTag() }
func (p prepared) Aligned() grafttask.Result { return p.p.Result }
func (p prepared) Tracks() int               { return p.p.Tracks() }

// PrepareGraft readies a graft to ride along with a transcode of source.
func (Engine) PrepareGraft(ctx context.Context, source string, t grafttask.Task, dataDir string) (grafttask.Prepared, grafttask.Result) {
	p, res := graft.Prepare(ctx, t, graft.Options{DataDir: dataDir}, source)
	if p == nil {
		return nil, res
	}
	return prepared{p}, grafttask.Result{}
}

// EncodeGraft is Encode with the prepared graft's track after the plan's
// audio, in the same pass.
func (Engine) EncodeGraft(ctx context.Context, plan standard.Result, tier transcode.Tier, input, output string,
	g grafttask.Prepared, progress func(transcode.Progress),
) (string, error) {
	gp, ok := g.(prepared)
	if !ok {
		return "", fmt.Errorf("inprocess: a graft prepared elsewhere (%T)", g)
	}
	dev, err := openDevice(tier)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", transcode.ErrDeviceUnavailable, tier, err)
	}
	if dev != nil {
		defer func() { _ = dev.Close() }()
	}
	_, err = engine.Run(ctx, plan, input, output, engine.Options{HWDevice: dev, Progress: progress, Graft: gp.p.Audio()})
	if err == nil {
		return "", nil
	}
	var ee *engine.Error
	if errors.As(err, &ee) {
		return ee.LogTail, err
	}
	return "", err
}

// CheckGraft verifies the grafted track of output.
func (Engine) CheckGraft(ctx context.Context, g grafttask.Prepared, output string, graftedIndex, total int) grafttask.Result {
	gp, ok := g.(prepared)
	if !ok {
		return grafttask.Failed(grafttask.ReasonError, "inprocess: a graft prepared elsewhere (%T)", g)
	}
	return graft.Check(ctx, gp.p, output, graftedIndex, total)
}
