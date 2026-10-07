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

// Package index is the IPTVChannel field index both Live TV controllers
// find a provider's channels by (spec 2026-10-07 §5.1): a namespace List per
// event would scale as watched × listed, the gotcha the startup map
// functions hit.
package index

import (
	"context"
	"fmt"
	"sync"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
)

// ChannelByProvider is the field index an IPTVChannel is found under by its
// spec.providerRef.
const ChannelByProvider = "livetv.spec.providerRef"

// Extract is the [ChannelByProvider] indexer.
func Extract(o client.Object) []string {
	ch, ok := o.(*clustarrv1.IPTVChannel)
	if !ok || ch.Spec.ProviderRef == "" {
		return nil
	}
	return []string{ch.Spec.ProviderRef}
}

// ChannelsOf lists the IPTVChannels of provider in namespace through
// [ChannelByProvider].
func ChannelsOf(ctx context.Context, c client.Reader, namespace, provider string) ([]clustarrv1.IPTVChannel, error) {
	var list clustarrv1.IPTVChannelList
	if err := c.List(ctx, &list, client.InNamespace(namespace), client.MatchingFields{ChannelByProvider: provider}); err != nil {
		return nil, fmt.Errorf("livetv index: list IPTVChannels of %s/%s: %w", namespace, provider, err)
	}
	return list.Items, nil
}

var (
	mu         sync.Mutex
	registered = map[client.FieldIndexer]bool{}
)

// Register adds [ChannelByProvider] to mgr's cache once. Both controllers
// call it from SetupWithManager: a second IndexField for one (type, field)
// on one cache is an "indexer conflict" at start, so a repeat is a no-op.
func Register(ctx context.Context, mgr ctrl.Manager) error {
	mu.Lock()
	defer mu.Unlock()
	idx := mgr.GetFieldIndexer()
	if registered[idx] {
		return nil
	}
	if err := idx.IndexField(ctx, &clustarrv1.IPTVChannel{}, ChannelByProvider, Extract); err != nil {
		return fmt.Errorf("livetv index: index IPTVChannel by provider: %w", err)
	}
	registered[idx] = true
	return nil
}
