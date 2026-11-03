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

package rowcache

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"

	"github.com/klauspost/compress/zstd"
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

const (
	// dictionaryHistoryBytes is how much of the initial list a dictionary
	// carries as history: the sample rows concatenated, so every common
	// run of bytes in the kind -- field names, enum values, the API group,
	// the namespace -- is in reach of every row's encoder. Measured on the
	// owner's library (2026-11-03): rows of this kind compress 1.8-2.7x on
	// their own and 4.9-8.3x with such a dictionary, within 5% of one
	// trained by zstd's own trainer.
	dictionaryHistoryBytes = 128 << 10
	// dictionarySamples is how many rows feed the dictionary's entropy
	// tables and its history.
	dictionarySamples = 2000
	// minDictionaryRows is the fewest rows worth a dictionary.
	minDictionaryRows = 64
	// dictionaryID names the one dictionary a codec builds; a row records
	// the id it was compressed with (0: none) in [Row.dict].
	dictionaryID = 1

	// encoderWindow bounds the encoder's history, dictionary included; a
	// row is a few KB, so a small window costs nothing and keeps each
	// encoder state small.
	encoderWindow = 512 << 10
	// encoders is how many compressions may run at once: the watch
	// stream's transform and the dictionary sweep.
	encoders = 2
)

// codec turns one kind's objects into rows and back: JSON, then one zstd
// frame per row, compressed with the kind's dictionary once it has one.
type codec struct {
	kind string
	gvk  schema.GroupVersionKind
	// typ is the kind's struct type; a decode allocates one.
	typ reflect.Type

	mu   sync.RWMutex
	enc  *zstd.Encoder
	dec  *zstd.Decoder
	dict []byte
	// dictID is what the encoder compresses with: 0 until a dictionary is
	// built.
	dictID uint32

	decodes map[string]prometheus.Counter
}

// Decode reasons, the reason label of clustarr_rowcache_decodes_total.
const (
	reasonRead   = "read"
	reasonEvent  = "event"
	reasonIndex  = "index"
	reasonSample = "sample"
)

func newCodec(kind string, gvk schema.GroupVersionKind, typ reflect.Type) (*codec, error) {
	enc, err := zstd.NewWriter(nil, encoderOptions(nil)...)
	if err != nil {
		return nil, fmt.Errorf("rowcache: %s: encoder: %w", kind, err)
	}
	dec, err := zstd.NewReader(nil, decoderOptions(nil)...)
	if err != nil {
		return nil, fmt.Errorf("rowcache: %s: decoder: %w", kind, err)
	}
	c := &codec{kind: kind, gvk: gvk, typ: typ, enc: enc, dec: dec, decodes: map[string]prometheus.Counter{}}
	for _, reason := range []string{reasonRead, reasonEvent, reasonIndex, reasonSample} {
		c.decodes[reason] = metrics.RowCacheDecodesTotal.WithLabelValues(kind, reason)
	}
	return c, nil
}

func encoderOptions(dict []byte) []zstd.EOption {
	opts := []zstd.EOption{
		zstd.WithEncoderLevel(zstd.SpeedDefault),
		zstd.WithEncoderConcurrency(encoders),
		zstd.WithWindowSize(encoderWindow),
		zstd.WithLowerEncoderMem(true),
		zstd.WithEncoderCRC(false),
	}
	if dict != nil {
		opts = append(opts, zstd.WithEncoderDict(dict))
	}
	return opts
}

func decoderOptions(dict []byte) []zstd.DOption {
	opts := []zstd.DOption{
		zstd.WithDecoderConcurrency(0),
		zstd.WithDecoderLowmem(true),
		zstd.WithDecoderMaxWindow(encoderWindow),
	}
	if dict != nil {
		opts = append(opts, zstd.WithDecoderDicts(dict))
	}
	return opts
}

// encode serializes obj and compresses it with the codec's current
// dictionary. The body is exactly sized: a row keeps it for the object's
// whole life.
func (c *codec) encode(obj runtime.Object) (body []byte, dict uint32, size int, err error) {
	js, err := json.Marshal(obj)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("rowcache: %s: encode: %w", c.kind, err)
	}
	body, dict = c.compress(js)
	return body, dict, len(js), nil
}

func (c *codec) compress(js []byte) ([]byte, uint32) {
	c.mu.RLock()
	enc, dict := c.enc, c.dictID
	c.mu.RUnlock()
	scratch := scratchPool.Get().(*[]byte)
	out := enc.EncodeAll(js, (*scratch)[:0])
	body := make([]byte, len(out))
	copy(body, out)
	*scratch = out[:0]
	scratchPool.Put(scratch)
	return body, dict
}

var scratchPool = sync.Pool{New: func() any {
	b := make([]byte, 0, 16<<10)
	return &b
}}

// decodeJSON inflates a row's body.
func (c *codec) decodeJSON(r *Row) ([]byte, error) {
	c.mu.RLock()
	dec := c.dec
	c.mu.RUnlock()
	js, err := dec.DecodeAll(r.body, make([]byte, 0, r.size))
	if err != nil {
		return nil, fmt.Errorf("rowcache: %s: %s: inflate: %w", c.kind, r.key(), err)
	}
	return js, nil
}

// decode builds a fresh object of the kind from a row, its GroupVersionKind
// set as the informer cache sets it.
func (c *codec) decode(r *Row, reason string) (runtime.Object, error) {
	obj, ok := reflect.TypeAssert[runtime.Object](reflect.New(c.typ))
	if !ok {
		return nil, fmt.Errorf("rowcache: %s: %s is not a runtime.Object", c.kind, c.typ)
	}
	if err := c.decodeInto(r, obj, reason); err != nil {
		return nil, err
	}
	return obj, nil
}

// decodeInto fills out, zeroed first, from a row. out must be a pointer to
// the kind's struct. The inflated JSON lives in a pooled buffer for the
// unmarshal alone: encoding/json copies every string and byte slice out of
// its input, so nothing of the object points into it.
func (c *codec) decodeInto(r *Row, out runtime.Object, reason string) error {
	v := reflect.ValueOf(out)
	if v.Kind() != reflect.Pointer || v.Elem().Type() != c.typ {
		return fmt.Errorf("rowcache: %s: cache had type *%s, but %T was asked for", c.kind, c.typ, out)
	}
	c.mu.RLock()
	dec := c.dec
	c.mu.RUnlock()
	buf := inflatePool.Get().(*[]byte)
	js, err := dec.DecodeAll(r.body, (*buf)[:0])
	if err != nil {
		inflatePool.Put(buf)
		return fmt.Errorf("rowcache: %s: %s: inflate: %w", c.kind, r.key(), err)
	}
	v.Elem().Set(reflect.Zero(c.typ))
	err = json.Unmarshal(js, out)
	*buf = js[:0]
	inflatePool.Put(buf)
	if err != nil {
		return fmt.Errorf("rowcache: %s: %s: decode: %w", c.kind, r.key(), err)
	}
	out.GetObjectKind().SetGroupVersionKind(c.gvk)
	c.decodes[reason].Inc()
	return nil
}

var inflatePool = sync.Pool{New: func() any {
	b := make([]byte, 0, 8<<10)
	return &b
}}

// recompress returns the row compressed with the codec's current
// dictionary, every other field the same.
func (c *codec) recompress(r *Row) (*Row, error) {
	js, err := c.decodeJSON(r)
	if err != nil {
		return nil, err
	}
	body, dict := c.compress(js)
	nr := *r
	nr.body, nr.dict = body, dict
	return &nr, nil
}

// buildDictionary makes the kind's dictionary from sample rows (their
// JSON, in a random order) and switches the encoder to it. The decoder
// keeps reading rows compressed before it: a frame names its dictionary.
func (c *codec) buildDictionary(samples [][]byte) error {
	var history []byte
	for _, s := range samples {
		if len(history) >= dictionaryHistoryBytes {
			break
		}
		history = append(history, s...)
	}
	dict, err := zstd.BuildDict(zstd.BuildDictOptions{
		ID:       dictionaryID,
		Contents: samples,
		History:  history,
		Level:    zstd.SpeedDefault,
	})
	if err != nil {
		return fmt.Errorf("rowcache: %s: build dictionary: %w", c.kind, err)
	}
	enc, err := zstd.NewWriter(nil, encoderOptions(dict)...)
	if err != nil {
		return fmt.Errorf("rowcache: %s: encoder with dictionary: %w", c.kind, err)
	}
	dec, err := zstd.NewReader(nil, decoderOptions(dict)...)
	if err != nil {
		return fmt.Errorf("rowcache: %s: decoder with dictionary: %w", c.kind, err)
	}
	c.mu.Lock()
	// The old encoder and decoder are left to the collector: a decode in
	// flight may still hold one, and Close would pull its buffers away.
	c.enc, c.dec, c.dict, c.dictID = enc, dec, dict, dictionaryID
	c.mu.Unlock()
	metrics.RowCacheDictionaryBytes.WithLabelValues(c.kind).Set(float64(len(dict)))
	return nil
}

// dictionary reports the id of the dictionary new rows are compressed with.
func (c *codec) dictionary() uint32 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.dictID
}
