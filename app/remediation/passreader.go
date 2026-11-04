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

package remediation

import (
	"context"
	"reflect"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// passReader memoises one pass's Gets. The planners gather on their own,
// and several read the file's item and its Series -- the transcode, naming,
// graft and subtitles planners and the exclusion check -- each a decode of
// the row cache: about 77 decodes a file pass on talos (2026-11-03), 29% of
// the manager's CPU under a backlog, and the allocation the collector
// chased at every GOGC. A repeat Get of one object within the pass is
// answered from the first decode, by the same object, as
// client.UnsafeDisableDeepCopy answers it: the planners never mutate what
// they read, as the row cache's handlers never do. Lists pass through. A
// NotFound is remembered too; the pass is one instant.
type passReader struct {
	client.Reader
	mu  sync.Mutex
	got map[passKey]*passEntry
}

type passKey struct {
	typ reflect.Type
	key client.ObjectKey
}

// passEntry is the first Get's answer: a shallow copy of the object's
// struct, so a later caller's top-level fields are its own while the
// slices and maps beneath are shared.
type passEntry struct {
	obj reflect.Value
	err error
}

func newPassReader(r client.Reader) *passReader {
	return &passReader{Reader: r, got: map[passKey]*passEntry{}}
}

// Get implements client.Reader.
func (r *passReader) Get(ctx context.Context, key client.ObjectKey, out client.Object, opts ...client.GetOption) error {
	k := passKey{typ: reflect.TypeOf(out), key: key}
	kind := out.GetObjectKind().GroupVersionKind().Kind
	if kind == "" {
		kind = k.typ.Elem().Name()
	}
	r.mu.Lock()
	e, ok := r.got[k]
	r.mu.Unlock()
	if ok {
		metrics.RemediationPassReadsTotal.WithLabelValues(kind, "memo").Inc()
		if e.err != nil {
			return e.err
		}
		reflect.ValueOf(out).Elem().Set(e.obj)
		return nil
	}
	metrics.RemediationPassReadsTotal.WithLabelValues(kind, "read").Inc()
	err := r.Reader.Get(ctx, key, out, opts...)
	e = &passEntry{err: err}
	if err == nil {
		v := reflect.ValueOf(out).Elem()
		e.obj = reflect.New(v.Type()).Elem()
		e.obj.Set(v)
	}
	r.mu.Lock()
	r.got[k] = e
	r.mu.Unlock()
	return err
}
