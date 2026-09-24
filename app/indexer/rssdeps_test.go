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

package indexarr

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/indexer/controller/indexer"
	"github.com/mediactl/clustarr/app/indexer/search"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// The RSS poll production builds counts its requests into the SAME query ring
// the search fan-out counts into. A nil CountQuery is what a unit test uses to
// switch accounting off, so a wiring that forgot it would compile, run, and
// quietly leave every poll out of status.queriesInWindow again.
func TestTheRSSPollCountsIntoTheSearchQueryRing(t *testing.T) {
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	t.Cleanup(func() { _ = bus.Close() })
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()

	deps := rssDeps(c, bus, nil, indexer.NewClientCache(c, nil))
	require.NotNil(t, deps.CountQuery, "run.go must wire the RSS poll's query accounting")

	idx := &indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "idx", UID: "uid-idx"}}
	n, err := deps.CountQuery(t.Context(), idx, time.Now())
	require.NoError(t, err)
	require.Equal(t, int32(1), n)

	ent, err := bus.KV(events.BucketIndexerLimits).Get(t.Context(), search.QueryRingKey(string(idx.UID)))
	require.NoError(t, err, "the count must land in the search fan-out's own ring key")
	var ring []int64
	require.NoError(t, json.Unmarshal(ent.Value, &ring))
	require.Len(t, ring, 1)
}
