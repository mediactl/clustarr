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

package k8s_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

type informerGetter struct {
	mu    sync.Mutex
	kinds []string
	err   error
}

func (g *informerGetter) GetInformer(_ context.Context, obj client.Object, _ ...cache.InformerGetOption) (cache.Informer, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.kinds = append(g.kinds, fmt.Sprintf("%T", obj))
	return nil, g.err
}

func (g *informerGetter) seen() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.kinds...)
}

// TestInformerWarmerAsksForEachKind: a standby keeps the failover kinds'
// informers warm, so a failover does not wait out the library's initial
// list (control-plane placement design §5.4.1).
func TestInformerWarmerAsksForEachKind(t *testing.T) {
	g := &informerGetter{}
	w := k8s.InformerWarmer(g, &catalogv1alpha1.MediaFile{}, &catalogv1alpha1.Episode{},
		&catalogv1alpha1.Movie{}, &catalogv1alpha1.Series{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()

	require.Eventually(t, func() bool { return len(g.seen()) == 4 }, time.Second, 5*time.Millisecond)
	assert.Equal(t, []string{"*v1alpha1.MediaFile", "*v1alpha1.Episode", "*v1alpha1.Movie", "*v1alpha1.Series"}, g.seen())
	select {
	case <-done:
		t.Fatal("the warmer returned before its context ended")
	default:
	}
	cancel()
	require.NoError(t, <-done)
}

func TestInformerWarmerRunsOnEveryReplica(t *testing.T) {
	w := k8s.InformerWarmer(&informerGetter{})
	ler, ok := w.(manager.LeaderElectionRunnable)
	require.True(t, ok)
	assert.False(t, ler.NeedLeaderElection(), "a standby is the replica that needs it")
}

func TestInformerWarmerReportsAFailedInformer(t *testing.T) {
	g := &informerGetter{err: errors.New("no kind")}
	err := k8s.InformerWarmer(g, &catalogv1alpha1.MediaFile{}).Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MediaFile")
}

// TestLeaderGaugeFollowsElection is clustarr_manager_leader: 0 until this
// replica is elected, then 1 (control-plane placement design §5.4).
func TestLeaderGaugeFollowsElection(t *testing.T) {
	var mu sync.Mutex
	var values []float64
	set := func(v float64) { mu.Lock(); values = append(values, v); mu.Unlock() }
	got := func() []float64 { mu.Lock(); defer mu.Unlock(); return append([]float64(nil), values...) }
	elected := make(chan struct{})
	r := k8s.LeaderGauge(elected, set)
	assert.False(t, r.NeedLeaderElection(), "a standby reports 0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	require.Eventually(t, func() bool { return len(got()) == 1 }, time.Second, 5*time.Millisecond)
	assert.Equal(t, []float64{0}, got())
	close(elected)
	require.Eventually(t, func() bool { return len(got()) == 2 }, time.Second, 5*time.Millisecond)
	assert.Equal(t, []float64{0, 1}, got())
	cancel()
	require.NoError(t, <-done)
}
