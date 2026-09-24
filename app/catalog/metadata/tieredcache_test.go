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

package metadata

import (
	"context"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// counterValue reads the current value of a single-series counter (already
// resolved by WithLabelValues) without pulling in
// prometheus/client_golang/prometheus/testutil: that package needs
// github.com/kylelemons/godebug, which is in go.sum (a transitive dependency
// of testutil pulled in incidentally) but not declared indirect in go.mod,
// so `go test` refuses to build under -mod=readonly. go.mod/go.sum belong to
// the controller (CLAUDE.md, global-constraints.md), not this task, and this
// task must not run `go mod tidy`. client_model, used here, is already a
// declared indirect dependency (prometheus's own core package needs it), so
// this sidesteps the gap instead of touching go.mod.
func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, c.Write(&m))
	return m.GetCounter().GetValue()
}

// TestTieredCachePrefersL1ThenBackfillsFromL2 exercises the two-tier cache
// against the real metrics.MetadataCacheHitsTotal series. That series
// already exists in pkg/obs/metrics/domain.go (added by Phase C's serial
// setup task, ahead of this one) labelled {tier, outcome} — not the single
// {tier} label this task's brief assumed when it was written, and this task
// must not edit pkg/obs/metrics at all. tier names which cache layer was
// checked (l1 is checked on every Get; l2 only when l1 misses); outcome is
// hit or miss for that check. docs/observability.md's own recording rule
// for this series, `sum by (tier) (rate(...{outcome="hit"}[1h])) /
// sum by (tier) (rate(...[1h]))`, only makes sense under that reading.
func TestTieredCachePrefersL1ThenBackfillsFromL2(t *testing.T) {
	clock := clockwork.NewFakeClock()
	ctx := context.Background()

	l1, err := newTestLRU(t, clock)
	require.NoError(t, err)
	l2 := newKVCache(newTestKV(t, clock), clock)
	tc := newTieredCache(l1, l2)

	beforeL1Miss := counterValue(t, metrics.MetadataCacheHitsTotal.WithLabelValues("l1", "miss"))
	beforeL2Miss := counterValue(t, metrics.MetadataCacheHitsTotal.WithLabelValues("l2", "miss"))
	var out cacheFixture
	hit, err := tc.Get(ctx, "movie:tmdb=27205", &out)
	require.NoError(t, err)
	require.False(t, hit)
	require.Equal(t, beforeL1Miss+1, counterValue(t, metrics.MetadataCacheHitsTotal.WithLabelValues("l1", "miss")))
	require.Equal(t, beforeL2Miss+1, counterValue(t, metrics.MetadataCacheHitsTotal.WithLabelValues("l2", "miss")))

	require.NoError(t, tc.Set(ctx, "movie:tmdb=27205", cacheFixture{Title: "Inception"}, time.Hour))

	// L1 hit: direct write above put it in both tiers.
	beforeL1Hit := counterValue(t, metrics.MetadataCacheHitsTotal.WithLabelValues("l1", "hit"))
	hit, err = tc.Get(ctx, "movie:tmdb=27205", &out)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, "Inception", out.Title)
	require.Equal(t, beforeL1Hit+1, counterValue(t, metrics.MetadataCacheHitsTotal.WithLabelValues("l1", "hit")))

	// Simulate an L1 eviction (process restart): only L2 has it. Get must
	// still succeed, be counted as an l1 miss followed by an l2 hit, and
	// backfill L1.
	l1Only, err := newTestLRU(t, clock)
	require.NoError(t, err)
	tc2 := newTieredCache(l1Only, l2)
	beforeL2Hit := counterValue(t, metrics.MetadataCacheHitsTotal.WithLabelValues("l2", "hit"))
	hit, err = tc2.Get(ctx, "movie:tmdb=27205", &out)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, beforeL2Hit+1, counterValue(t, metrics.MetadataCacheHitsTotal.WithLabelValues("l2", "hit")))
}
