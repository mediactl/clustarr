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

package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/mediactl/clustarr/pkg/ffruntime"
)

// guard wraps a task's decoder: after a call the runtime abandoned, every
// later call fails at once with the same error (spec §6.6). An abandoned
// call is still running in this process, so redelivering the task would
// start another stuck call, and at ffruntime.MaxAbandoned restart the pod.
type guard struct {
	Decoder
	mu        sync.Mutex
	abandoned error
}

var errSkipped = errors.New("not decoded: an earlier FFmpeg call in this task was abandoned")

func (g *guard) check() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.abandoned != nil {
		return fmt.Errorf("%w (%w)", errSkipped, g.abandoned)
	}
	return nil
}

func (g *guard) note(err error) error {
	if errors.Is(err, ffruntime.ErrAbandoned) || errors.Is(err, ffruntime.ErrWedged) {
		g.mu.Lock()
		if g.abandoned == nil {
			g.abandoned = err
		}
		g.mu.Unlock()
	}
	return err
}

// failed is the task's first abandoned call, nil when there was none.
func (g *guard) failed() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.abandoned
}

func (g *guard) Audio(ctx context.Context, path string, stream int, fromS, durS float64) ([]int16, error) {
	if err := g.check(); err != nil {
		return nil, err
	}
	pcm, err := g.Decoder.Audio(ctx, path, stream, fromS, durS)
	return pcm, g.note(err)
}

func (g *guard) Frames(ctx context.Context, path string, fromS float64) ([][]byte, error) {
	if err := g.check(); err != nil {
		return nil, err
	}
	fr, err := g.Decoder.Frames(ctx, path, fromS)
	return fr, g.note(err)
}

func (g *guard) Frame(ctx context.Context, path string, atS float64) ([]byte, error) {
	if err := g.check(); err != nil {
		return nil, err
	}
	b, err := g.Decoder.Frame(ctx, path, atS)
	return b, g.note(err)
}
