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
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// gatewayProbe counts the gateways a supervisor starts and stops, and feeds
// it the fingerprints a test scripts.
type gatewayProbe struct {
	mu      sync.Mutex
	fps     []string // returned in order; the last repeats
	listErr error    // returned after the first fingerprint, when set
	calls   int
	starts  atomic.Int32
	stops   atomic.Int32
}

func (g *gatewayProbe) fingerprint(context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	if g.listErr != nil && g.calls > 1 {
		return "", g.listErr
	}
	fp := g.fps[0]
	if len(g.fps) > 1 {
		g.fps = g.fps[1:]
	}
	return fp, nil
}

func (g *gatewayProbe) start(context.Context) (func(), error) {
	g.starts.Add(1)
	return func() { g.stops.Add(1) }, nil
}

// supervise runs superviseGateway until settled says so, then cancels it
// and returns its error.
func supervise(t *testing.T, g *gatewayProbe, settled func() bool) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- superviseGateway(ctx, time.Millisecond, g.fingerprint, g.start) }()
	require.Eventually(t, settled, 5*time.Second, time.Millisecond)
	cancel()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("superviseGateway did not return once its context ended")
		return nil
	}
}

// TestTheGatewayIsRebuiltWhenItsProvidersChange: Setup reads the
// MetadataProviders once, so a provider added after the agent started -- a
// fresh install's TMDB and TVDB, applied a minute after the chart (talos,
// 2026-10-15) -- was never asked. A changed provider set stops the running
// gateway and starts one built from the new set, once.
func TestTheGatewayIsRebuiltWhenItsProvidersChange(t *testing.T) {
	g := &gatewayProbe{fps: []string{"seeded", "seeded", "seeded+tmdb+tvdb"}}
	require.NoError(t, supervise(t, g, func() bool {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.starts.Load() == 2 && g.calls > 5
	}))
	require.EqualValues(t, 2, g.starts.Load(), "one rebuild for one change")
	require.EqualValues(t, 2, g.stops.Load(), "the first gateway stopped before the second started, the second when the context ended")
}

// TestTheGatewayIsNotRebuiltWhileItsProvidersStand: an unchanged set, or a
// List that fails, keeps the running gateway -- a rebuild drops the
// subscription and the responders for its duration.
func TestTheGatewayIsNotRebuiltWhileItsProvidersStand(t *testing.T) {
	for name, g := range map[string]*gatewayProbe{
		"unchanged":    {fps: []string{"seeded+tmdb+tvdb"}},
		"list failing": {fps: []string{"seeded+tmdb+tvdb"}, listErr: errors.New("cache not synced")},
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, supervise(t, g, func() bool {
				g.mu.Lock()
				defer g.mu.Unlock()
				return g.calls > 5
			}))
			require.EqualValues(t, 1, g.starts.Load())
			require.EqualValues(t, 1, g.stops.Load(), "stopped once, when the context ended")
		})
	}
}

// TestProviderFingerprintFollowsWhatTheRegistryReads: BuildRegistry reads
// every MetadataProvider's spec, so a provider added or removed, or a spec
// edited (its generation), changes the fingerprint; the List's order and a
// status write do not.
func TestProviderFingerprintFollowsWhatTheRegistryReads(t *testing.T) {
	p := func(name string, gen int64) catalogv1alpha1.MetadataProvider {
		return catalogv1alpha1.MetadataProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "clustarr-system", Name: name, Generation: gen}}
	}
	base := providerFingerprint([]catalogv1alpha1.MetadataProvider{p("musicbrainz", 1), p("tmdb", 1)})
	require.Equal(t, base, providerFingerprint([]catalogv1alpha1.MetadataProvider{p("tmdb", 1), p("musicbrainz", 1)}), "order")
	withStatus := p("tmdb", 1)
	withStatus.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}
	require.Equal(t, base, providerFingerprint([]catalogv1alpha1.MetadataProvider{p("musicbrainz", 1), withStatus}), "a status write")
	require.NotEqual(t, base, providerFingerprint([]catalogv1alpha1.MetadataProvider{p("musicbrainz", 1), p("tmdb", 1), p("tvdb", 1)}), "a provider added")
	require.NotEqual(t, base, providerFingerprint([]catalogv1alpha1.MetadataProvider{p("musicbrainz", 1)}), "a provider removed")
	require.NotEqual(t, base, providerFingerprint([]catalogv1alpha1.MetadataProvider{p("musicbrainz", 1), p("tmdb", 2)}), "a spec edited")
}
