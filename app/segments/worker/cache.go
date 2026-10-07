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
	"bytes"
	"context"
	"encoding/binary"
	"io"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/segments"
	"github.com/mediactl/clustarr/pkg/segments/chromaprint"
)

// fingerprint fills f.start and f.end from the store, or by decoding and
// fingerprinting the windows and storing them. A decode failure is f.err.
func (h *Handler) fingerprint(ctx context.Context, f *file) {
	startLen := startWindow(f.durS)
	endFrom, endLen := endWindow(f.durS, false)
	f.start, f.err = h.window(ctx, f, "start", 0, startLen)
	if f.err != nil {
		return
	}
	f.end, f.err = h.window(ctx, f, "end", endFrom, endLen)
	if f.err != nil {
		f.start = nil
	}
}

func (h *Handler) window(ctx context.Context, f *file, which string, fromS, lenS float64) ([]uint32, error) {
	name := segments.FingerprintKey(f.ProbeHash, which)
	if fp, ok := h.cached(ctx, name); ok {
		return fp, nil
	}
	pcm, err := h.Decoder.Audio(ctx, f.Path, int(f.AudioStream), fromS, lenS)
	if err != nil {
		return nil, err
	}
	fp := chromaprint.Fingerprint(pcm)
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, fp)
	if h.Fingerprints != nil {
		_, _ = h.Fingerprints.Put(ctx, name, &buf, events.ObjectMeta{}) // a cache: a failed write is recomputed next time
	}
	return fp, nil
}

func (h *Handler) cached(ctx context.Context, name string) ([]uint32, bool) {
	if h.Fingerprints == nil {
		return nil, false
	}
	_, r, err := h.Fingerprints.Get(ctx, name)
	if err != nil { // ErrObjectNotFound, or a store blip: decode instead
		return nil, false
	}
	defer func() { _ = r.Close() }()
	b, err := io.ReadAll(r)
	if err != nil || len(b)%4 != 0 {
		return nil, false
	}
	fp := make([]uint32, len(b)/4)
	for i := range fp {
		fp[i] = binary.LittleEndian.Uint32(b[4*i:])
	}
	return fp, true
}
