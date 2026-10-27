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

package agent

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/mediactl/clustarr/app/caption/providerset/build"
	"github.com/mediactl/clustarr/pkg/subtitles/ocr"
)

// ORTLibEnv names ONNX Runtime's shared library, as the native image and
// hack/dev-run.sh set it; markers reads the same variable.
const ORTLibEnv = "ORT_LIB_PATH"

// registerOCR gives the provider builder the in-process PGS reader behind
// the ocr SubtitleProvider: PaddleOCR's recognition model on ONNX Runtime
// from libPath, loaded at the first read, so a cluster with no ocr provider
// pays nothing for it. A runtime that cannot be loaded fails every read
// with ocr.ErrUnavailable, which the fetch task records against the
// language; `agent --self-check` is where the image proves it carries the
// runtime.
func registerOCR(b *build.Builder, libPath string) {
	r := &imageReader{lib: libPath}
	b.ReadImage = r.Read
}

// imageReader is a pgsocr.ReadFunc over one lazily built recognizer. It
// reads one track at a time: the fetch worker runs up to sixteen tasks at
// once and the recognizer serialises only single lines, so on 2026-10-27
// fourteen tracks of The Sopranos interleaved line by line on the pod's
// one CPU, each holding its own bitmaps, and none finished for a quarter
// of an hour. A task waits here with its file open, not read.
type imageReader struct {
	lib  string
	once sync.Once
	rec  *ocr.Recognizer
	err  error
	mu   sync.Mutex
}

// Read reads a .sup as SubRip.
func (r *imageReader) Read(ctx context.Context, sup io.Reader) ([]byte, error) {
	r.once.Do(func() { r.rec, r.err = ocr.New(r.lib) })
	if r.err != nil {
		return nil, fmt.Errorf("%s=%q: %w", ORTLibEnv, r.lib, r.err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cues, err := r.rec.Read(ctx, sup)
	if err != nil {
		return nil, err
	}
	return ocr.SRT(cues), nil
}
