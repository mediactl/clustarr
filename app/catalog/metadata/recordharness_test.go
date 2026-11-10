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

package metadata_test

import (
	"context"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/itemstatus"
	"github.com/mediactl/clustarr/app/catalog/metadata"
	"github.com/mediactl/clustarr/app/catalog/metadatainputs"
	"github.com/mediactl/clustarr/app/catalog/metadataset"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// recordHarness is the item-metadata record path every Handler test has
// needed since 01a223a4 (ADR-0019 §7.1, wave A5.1, 2026-10-07): the gateway
// writes no Kubernetes object and answers in clustarr-item-metadata, a
// records.Writer over a membus bus here, and the manager's metadata stage
// (app/remediation/metadata: metadataset.Build, itemstatus.ApplySet)
// incorporates the record into status.metadata under catalog-metadata. A
// test reads the gateway's half off the record (answered, record) and runs
// the manager's half itself (incorporate) when a later pass must find the
// earlier document on the apiserver object -- the ratings, ids and Plex
// seasons the Handler carries forward read off the target (knownRatings,
// knownExternalIDs, knownPlexSeasons), so a steady-state pass needs the
// first pass incorporated, as it is on a cluster.
type recordHarness struct {
	bus     events.Bus
	records *records.Writer[*schema.ItemMetadataRecord]
	reader  *records.Reader[*schema.ItemMetadataRecord]
}

func newRecordHarness(t *testing.T) recordHarness {
	t.Helper()
	bus := membus.New(clockwork.NewRealClock())
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	kv := bus.KV(events.BucketItemMetadata)
	return recordHarness{
		bus:     bus,
		records: records.NewWriter(kv, agentrecords.ItemMetadata(), "test", "v0"),
		reader:  records.NewReader(kv, agentrecords.ItemMetadata()),
	}
}

// handle runs one MetadataTask for ns/name through handler and returns the
// record it answered, keyed by name: the fake-client tests give each object
// its name as its UID. An envtest object's UID is the apiserver's, so those
// tests run handleTask and read the record with answered.
func (h recordHarness) handle(t *testing.T, handler *metadata.Handler, kind commonv1.MediaKind, ns, name string) *schema.ItemMetadataRecord {
	t.Helper()
	env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
	var err error
	_, env.Data, err = schema.Encode(schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: kind, Name: name}})
	require.NoError(t, err)
	require.NoError(t, handler.Handle(context.Background(), testMessage{env: env}))
	return h.record(t, name)
}

// record is the item's answered record, which must hold no failure.
func (h recordHarness) record(t *testing.T, uid string) *schema.ItemMetadataRecord {
	t.Helper()
	rec, _, ok, err := h.reader.Get(context.Background(), agentrecords.ItemMetadataKey(uid))
	require.NoError(t, err)
	require.True(t, ok, "the Handler answers in the item-metadata record")
	require.Empty(t, rec.Failure)
	return rec
}

// answered is the record the Handler answered for obj.
func (h recordHarness) answered(t *testing.T, obj client.Object) *schema.ItemMetadataRecord {
	t.Helper()
	return h.record(t, string(obj.GetUID()))
}

// incorporate applies rec onto obj exactly as the manager's metadata stage
// does on an item pass (app/remediation/metadata.Stage.Plan: the record
// must be metadatainputs.Incorporable -- a successful answer to the item's
// current ask, newer than its status.metadata at the second the apiserver
// keeps -- then metadataset.Build renders catalog-metadata's set and
// itemstatus.ApplySet lands it), and re-reads obj. A second pass of one
// test needs the Handler's clock stepped past the first record (stepClock),
// or the second record is not newer and nothing is incorporated.
func incorporate(t *testing.T, ctx context.Context, c client.Client, obj client.Object, rec *schema.ItemMetadataRecord) {
	t.Helper()
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(obj), obj))
	require.True(t, metadatainputs.Incorporable(obj, rec),
		"the record answers the item's current ask with a document newer than its status.metadata")
	set, owed := metadataset.Build(metadataset.Input{Item: obj, Record: rec})
	require.True(t, owed, "a record to incorporate owes the set")
	_, conflicted, err := itemstatus.ApplySet(ctx, c, obj, "", set)
	require.NoError(t, err)
	require.False(t, conflicted)
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(obj), obj))
}

// stepClock is a Handler.Now a test moves by hand: Step puts the next
// refresh a minute on, so its record reads newer than the document the
// previous pass incorporated (metadatainputs.Incorporable compares at
// second precision), as two refreshes on a cluster are minutes apart.
type stepClock struct{ now time.Time }

func newStepClock() *stepClock { return &stepClock{now: time.Now()} }

func (c *stepClock) Now() time.Time { return c.now }

func (c *stepClock) Step() { c.now = c.now.Add(time.Minute) }
