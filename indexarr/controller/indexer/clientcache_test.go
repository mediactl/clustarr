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

package indexer

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/ratelimit"
)

// countingClient counts Secret GETs, which is the whole point of the cache:
// indexarr disables the Secret informer, so every uncached build is a live
// apiserver round trip inside a per-indexer search timeout.
type countingClient struct {
	client.Client
	gets atomic.Int64
}

func (c *countingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*corev1.Secret); ok {
		c.gets.Add(1)
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func cacheFixture(t *testing.T) (*countingClient, *indexv1alpha1.Indexer) {
	t.Helper()
	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tracker", Namespace: "media", UID: "uid-1", ResourceVersion: "100",
		},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL:   "https://tracker.invalid",
			Generic:   &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolTorrent},
			SecretRef: &corev1.LocalObjectReference{Name: "tracker-creds"},
		},
	}
	scheme := runtime.NewScheme()
	require.NoError(t, k8s.AddToScheme(scheme))
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "tracker-creds", Namespace: "media"},
		Data:       map[string][]byte{"apikey": []byte("s3cret")},
	}).Build()
	return &countingClient{Client: base}, idx
}

// TestClientCacheServesAFanOutFromOneGet is the correctness half, not the
// performance half.
//
// ClientFor is called once per candidate indexer per search and once per RSS
// poll, and every uncached call is a live apiserver GET, because indexarr
// disables the Secret informer. The GET happens INSIDE the per-indexer search
// timeout, and a ClientFor error becomes a named failure outcome, which runs
// RecordFailure -- escalationLevel, then disabledUntil. So a throttled REST
// client or an apiserver blip could escalate a perfectly healthy indexer
// toward disabled. Caching removes the amplification that turns a blip into a
// backoff.
func TestClientCacheServesAFanOutFromOneGet(t *testing.T) {
	c, idx := cacheFixture(t)
	cc := NewClientCache(c, ratelimit.New(ratelimit.Config{}))

	first, err := cc.For(t.Context(), idx)
	require.NoError(t, err)
	require.NotNil(t, first)

	for range 99 {
		again, err := cc.For(t.Context(), idx)
		require.NoError(t, err)
		require.Same(t, first, again, "the cache rebuilt a client for an unchanged Indexer")
	}
	require.Equal(t, int64(1), c.gets.Load(),
		"a hundred-indexer fan-out made a hundred apiserver GETs for one indexer's Secret; "+
			"each one is inside the search timeout and a failure escalates that indexer")
}

// TestClientCacheRebuildsWhenTheIndexerChanges: the key is UID plus
// resourceVersion, so any edit -- a new baseURL, a new apiPath, a new
// secretRef -- is picked up on the next call rather than on the next TTL.
func TestClientCacheRebuildsWhenTheIndexerChanges(t *testing.T) {
	c, idx := cacheFixture(t)
	cc := NewClientCache(c, ratelimit.New(ratelimit.Config{}))

	first, err := cc.For(t.Context(), idx)
	require.NoError(t, err)

	edited := idx.DeepCopy()
	edited.ResourceVersion = "101"
	second, err := cc.For(t.Context(), edited)
	require.NoError(t, err)

	require.NotSame(t, first, second,
		"an edited Indexer kept its old client, so a changed baseURL or timeout never takes effect")
	require.Equal(t, int64(2), c.gets.Load())
}

// TestClientCacheExpiresSoARotatedSecretIsPickedUp is the bound on the one
// change the key cannot see.
//
// Rotating an apikey does not touch the Indexer, and with the Secret informer
// disabled there is no watch to invalidate on, so without a TTL a cached
// client would carry a revoked credential until the Indexer happened to be
// edited -- which for a healthy indexer is never.
func TestClientCacheExpiresSoARotatedSecretIsPickedUp(t *testing.T) {
	c, idx := cacheFixture(t)
	now := time.Now()
	cc := NewClientCache(c, ratelimit.New(ratelimit.Config{}))
	cc.Now = func() time.Time { return now }

	first, err := cc.For(t.Context(), idx)
	require.NoError(t, err)

	now = now.Add(DefaultClientCacheTTL - time.Second)
	same, err := cc.For(t.Context(), idx)
	require.NoError(t, err)
	require.Same(t, first, same, "the entry expired early")

	now = now.Add(2 * time.Second)
	fresh, err := cc.For(t.Context(), idx)
	require.NoError(t, err)
	require.NotSame(t, first, fresh,
		"the cached client never expires, so a rotated apikey is used until the Indexer is edited")
	require.Equal(t, int64(2), c.gets.Load())
}

// TestClientCacheForgetsADeletedIndexer keeps the map bounded by the live
// Indexer set rather than by every Indexer this process ever saw. A cached
// entry holds an *http.Client with its own Transport, so the leak is idle
// connections, not just a map entry.
func TestClientCacheForgetsADeletedIndexer(t *testing.T) {
	c, idx := cacheFixture(t)
	cc := NewClientCache(c, ratelimit.New(ratelimit.Config{}))

	_, err := cc.For(t.Context(), idx)
	require.NoError(t, err)
	require.Equal(t, 1, cc.Len())

	cc.Forget(idx.UID)
	require.Zero(t, cc.Len())

	// Keyed by UID: an Indexer deleted and recreated under the same name is a
	// different object and must not inherit a client.
	_, err = cc.For(t.Context(), idx)
	require.NoError(t, err)
	require.Equal(t, int64(2), c.gets.Load())
}

// TestClientCacheReportsAMissingSecret: a reference to a Secret that is not
// there is an error, not a silently unauthenticated client. Nothing is cached
// for it, so the next call retries rather than serving the failure.
func TestClientCacheReportsAMissingSecret(t *testing.T) {
	c, idx := cacheFixture(t)
	idx.Spec.SecretRef = &corev1.LocalObjectReference{Name: "absent"}
	cc := NewClientCache(c, ratelimit.New(ratelimit.Config{}))

	_, err := cc.For(t.Context(), idx)
	require.ErrorContains(t, err, "absent")
	require.Zero(t, cc.Len(), "a failed build was cached, so the next search serves the failure")
}

// TestClientCacheRejectsANilIndexer: the fan-out and the RSS poll both call
// this per message, and a nil dereference here is process-fatal for a service
// pinned to one replica.
func TestClientCacheRejectsANilIndexer(t *testing.T) {
	c, _ := cacheFixture(t)
	cc := NewClientCache(c, ratelimit.New(ratelimit.Config{}))
	require.NotPanics(t, func() {
		_, err := cc.For(context.Background(), nil)
		require.Error(t, err)
	})
}
