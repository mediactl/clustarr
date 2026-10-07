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

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/segments"
	"github.com/mediactl/clustarr/pkg/segments/chromaprint"
	"github.com/mediactl/clustarr/pkg/version"
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
	if fp, ok := h.cached(ctx, name, lenS); ok {
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
		// Self-describing, through the store's one builder (ADR-0021 §2). A
		// cache: a failed write is recomputed next time.
		meta := segments.FingerprintMeta(f.ProbeHash, which, lenS, h.writtenBy())
		_, _ = h.Fingerprints.Put(ctx, name, &buf, meta)
	}
	return fp, nil
}

// writtenBy is the fingerprints' clustarr.io/written-by: Handler.WrittenBy,
// else this binary's name and version.
func (h *Handler) writtenBy() string {
	if h.WrittenBy != "" {
		return h.WrittenBy
	}
	return "markers@" + version.String()
}

// cached reads name's fingerprint when its metadata matches what this
// process computes for a window of lenS seconds. It decides from Info, a
// direct get of the object's meta, and runs Get -- a consumer over the
// chunks -- only for a match; a mismatch is recomputed and overwritten
// (ADR-0021 §4).
func (h *Handler) cached(ctx context.Context, name string, lenS float64) ([]uint32, bool) {
	if h.Fingerprints == nil {
		return nil, false
	}
	info, err := h.Fingerprints.Info(ctx, name)
	if err != nil { // ErrObjectNotFound, or a store blip: decode instead
		return nil, false
	}
	if why := segments.FingerprintMetaMismatch(info, lenS); why != "" {
		logging.FromContext(ctx).Debug("segments: cached fingerprint does not match; recomputing", "name", name, "why", why)
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
