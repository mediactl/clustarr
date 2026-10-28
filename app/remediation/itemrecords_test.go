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
	"testing"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
)

// An item pass writes its request record as a file pass does: the
// image-set stage asks MediUX through a clustarr-image-sets request, and
// until 2026-10-28 the item runner refused the effect ("unknown item effect
// remediation.RecordWrite"), so no Movie or Series was ever asked once a
// mediux provider existed. A create at revision 0, an update at the read
// revision, and a revision another writer moved is a CAS miss the next pass
// re-plans from.
func TestAnItemPassWritesItsRequestRecord(t *testing.T) {
	ctx := context.Background()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	ir := &ItemReconciler{Env: &Env{Bus: bus}}
	kv := bus.KV(events.BucketImageSets)

	require.NoError(t, ir.runItemEffect(ctx, nil, RecordWrite{Bucket: events.BucketImageSets, Key: "u1", Value: []byte(`{"seq":1}`), Remediation: "imageset", Lane: "none"}))
	e, err := kv.Get(ctx, "u1")
	require.NoError(t, err)
	require.JSONEq(t, `{"seq":1}`, string(e.Value))

	require.NoError(t, ir.runItemEffect(ctx, nil, RecordWrite{Bucket: events.BucketImageSets, Key: "u1", Revision: e.Revision, Value: []byte(`{"seq":2}`)}))
	err = ir.runItemEffect(ctx, nil, RecordWrite{Bucket: events.BucketImageSets, Key: "u1", Revision: e.Revision, Value: []byte(`{"seq":3}`)})
	require.True(t, IsCASMiss(err), "a stale revision is a CAS miss: %v", err)
	err = ir.runItemEffect(ctx, nil, RecordWrite{Bucket: events.BucketImageSets, Key: "u1", Value: []byte(`{"seq":4}`)})
	require.True(t, IsCASMiss(err), "a create over a record is a CAS miss: %v", err)
}
