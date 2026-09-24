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

package natsbus

import (
	"context"
	"errors"
	"io"

	"github.com/mediactl/clustarr/pkg/events"
)

// errObjectStoreNotImplemented is what every notImplementedObjectStore
// method returns. Task B1 replaces this stub with a real binding onto
// jetstream.ObjectStore; task W0-3 only needs natsbus to keep satisfying the
// extended events.Bus interface.
var errObjectStoreNotImplemented = errors.New("object store: not implemented until B1")

// ObjectStore returns bucket's object store. Every method of the result
// fails with errObjectStoreNotImplemented until task B1 lands natsbus's
// jetstream.ObjectStore binding.
func (b *Bus) ObjectStore(bucket string) events.ObjectStore {
	return notImplementedObjectStore{}
}

// notImplementedObjectStore satisfies events.ObjectStore so natsbus compiles
// against the Bus interface task W0-3 added. See errObjectStoreNotImplemented.
type notImplementedObjectStore struct{}

var _ events.ObjectStore = notImplementedObjectStore{}

func (notImplementedObjectStore) Get(context.Context, string) (events.ObjectInfo, io.ReadCloser, error) {
	return events.ObjectInfo{}, nil, errObjectStoreNotImplemented
}

func (notImplementedObjectStore) Put(context.Context, string, io.Reader, map[string]string) (events.ObjectInfo, error) {
	return events.ObjectInfo{}, errObjectStoreNotImplemented
}

func (notImplementedObjectStore) Delete(context.Context, string) error {
	return errObjectStoreNotImplemented
}

func (notImplementedObjectStore) Info(context.Context, string) (events.ObjectInfo, error) {
	return events.ObjectInfo{}, errObjectStoreNotImplemented
}

func (notImplementedObjectStore) List(context.Context, string) ([]events.ObjectInfo, error) {
	return nil, errObjectStoreNotImplemented
}
