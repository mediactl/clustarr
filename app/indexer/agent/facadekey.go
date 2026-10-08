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

package agent

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/facadekey"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// The facade's API-key Secret, read by name. get only: the manager creates
// it when absent (app/indexer/manager, create-only, ADR-0019 §7.5, W31) and
// the index agent never writes it.
//
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

// FacadeAPIKeyField is facadekey.APIKeyField, the data key the manager stores a
// generated key under.
const FacadeAPIKeyField = facadekey.APIKeyField

// FacadeKeyRereadInterval is how often the index agent re-reads the facade's
// API-key Secret (ADR-0019 §7.5): until the manager has created it the
// facade answers 503, and afterwards a rotation reaches it without a
// restart.
const FacadeKeyRereadInterval = 30 * time.Second

// FacadeKeys is the Torznab facade's live API-key set: the non-blank entries
// of the Secret --facade-api-key-secret names, re-read by name every
// FacadeKeyRereadInterval. It fails closed: absent, unreadable or empty, the
// set is empty and the facade answers 503. The zero value holds no key.
type FacadeKeys struct {
	keys atomic.Pointer[[]string]
}

// Get returns the current keys (nil before the first read that found one).
func (k *FacadeKeys) Get() []string {
	if p := k.keys.Load(); p != nil {
		return *p
	}
	return nil
}

// Run re-reads Secret namespace/name through r (live: the agent never caches
// Secrets) at once and every interval until ctx ends, logging each change of
// state and never a key.
func (k *FacadeKeys) Run(ctx context.Context, r client.Reader, namespace, name string, interval time.Duration) error {
	log := logging.FromContext(ctx).With("secret", namespace+"/"+name)
	t := time.NewTicker(interval)
	defer t.Stop()
	had := -1
	for {
		keys, err := ReadFacadeAPIKeys(ctx, r, namespace, name)
		switch {
		case err != nil:
			log.Warn("index: reading the facade's API-key Secret failed; keeping the keys read last", "err", err)
		default:
			k.keys.Store(&keys)
			if len(keys) != had {
				if len(keys) == 0 {
					log.Warn("index: the facade has no API key yet and answers 503: the manager creates the " +
						"Secret when it is absent; a Secret with no non-blank entry is an operator's to fill")
				} else {
					log.Info("index: the facade's API keys are loaded", "keys", len(keys))
				}
				had = len(keys)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// ReadFacadeAPIKeys reads Secret namespace/name by name and returns its
// non-blank values (facadekey.KeysOf); an absent Secret is no key, not an
// error.
func ReadFacadeAPIKeys(ctx context.Context, r client.Reader, namespace, name string) ([]string, error) {
	var sec corev1.Secret
	key := types.NamespacedName{Namespace: namespace, Name: name}
	if err := r.Get(ctx, key, &sec); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("index: read the facade API-key Secret %s: %w", key, err)
	}
	return facadekey.KeysOf(&sec), nil
}
