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

package segmentplan

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Sweeper deletes clustarr-segments records whose MediaFile is gone, once a
// day (loop spec §4.11): the bucket's TTL is 0, so nothing else does. It
// lives here, not in app/catalog/segmenting, which F3.4 deletes (D-F3-2).
type Sweeper struct {
	KV     events.KV
	Reader client.Reader // the manager's cache, with mfindex.UID
	Every  time.Duration // 0 is a day
}

// Run sweeps every Every until ctx ends; it is added k8s.LeaderOnly.
func (s *Sweeper) Run(ctx context.Context) error {
	every := s.Every
	if every == 0 {
		every = 24 * time.Hour
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := s.Sweep(ctx); err != nil {
			logging.FromContext(ctx).Warn("segmentplan: sweep failed", "error", err)
		} else if n > 0 {
			logging.FromContext(ctx).Info("segmentplan: deleted the segments records of files that are gone", "records", n)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Sweep deletes, at the revision it read, each record whose UID (v2: its
// File.UID; v1: its key, ParseKVKeyToken) no MediaFile in the cache has.
func (s *Sweeper) Sweep(ctx context.Context) (int, error) {
	keys, err := s.KV.Keys(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, k := range keys {
		e, err := s.KV.Get(ctx, k)
		if errors.Is(err, events.ErrKeyNotFound) {
			continue
		}
		if err != nil {
			return n, err
		}
		uid, err := events.ParseKVKeyToken(k)
		var r struct {
			File schema.Ref `json:"file"`
		}
		if json.Unmarshal(e.Value, &r) == nil && r.File.UID != "" {
			uid, err = r.File.UID, nil
		}
		if err != nil {
			continue // not a record key: never guess
		}
		var files catalogv1alpha1.MediaFileList
		if err := s.Reader.List(ctx, &files, client.MatchingFields{mfindex.UID: uid}, client.UnsafeDisableDeepCopy); err != nil {
			return n, err
		}
		if len(files.Items) > 0 {
			continue
		}
		switch err := s.KV.DeleteRevision(ctx, k, e.Revision); {
		case errors.Is(err, events.ErrRevisionMismatch):
			continue // written since it was read: its writer knows a file we do not yet
		case err != nil:
			return n, err
		}
		n++
	}
	return n, nil
}
