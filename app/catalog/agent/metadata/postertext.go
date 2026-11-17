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

package metadata

import (
	"context"
	"fmt"
	"sync"

	"github.com/mediactl/clustarr/pkg/segments/textdet"
)

// ORTLibEnv names ONNX Runtime's shared library, as the native image and
// hack/dev-run.sh set it; the caption domain and markers read the same
// variable.
const ORTLibEnv = "ORT_LIB_PATH"

// posterText is the artwork fetcher's text detector (artwork.Fetcher.Text):
// PaddleOCR's text-detection model on ONNX Runtime from lib, the one markers
// finds credits with (pkg/segments/textdet), loaded at the first titled
// poster, as the caption domain loads its recognizer at the first image
// subtitle -- so a library with no anime pays nothing for it. A runtime that
// cannot be loaded fails every titled poster with textdet.ErrUnavailable,
// which the fetch records against the poster and which keeps the one
// already stored; `agent --self-check` is where the image proves it carries
// the runtime. The detector runs one image at a time by itself.
type posterText struct {
	lib  string
	once sync.Once
	det  textdet.Detector
	err  error
}

// Density implements artwork.Fetcher.Text.
func (p *posterText) Density(ctx context.Context, rgb []byte, w, h int) (int32, error) {
	p.once.Do(func() { p.det, p.err = textdet.NewONNX(ctx, p.lib) })
	if p.err != nil {
		return 0, fmt.Errorf("%s=%q: %w", ORTLibEnv, p.lib, p.err)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return p.det.Density(rgb, w, h)
}
