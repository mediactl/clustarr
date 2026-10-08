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

package manager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/facadekey"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// The facade's API-key Secret: read, and created when it is absent. get and
// create only -- the manager never updates or patches it, because a key an
// operator set must never be overwritten by one this process generated.
//
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create

// facadeAPIKeyBytes is a generated key's entropy: 32 random bytes, sent as
// 64 hex characters, which is the shape every Torznab client's apikey field
// accepts.
const facadeAPIKeyBytes = 32

// facadeKeyRetry is how long the leader waits before trying again when the
// Secret could not be read or created.
const facadeKeyRetry = 30 * time.Second

// EnsureFacadeKey creates Secret namespace/name with one random key under
// facadekey.APIKeyField when it does not exist (ADR-0019 §7.5, W31):
// create-only, under k8s.ManagerIndex. The index agent's Torznab facade
// fails closed and answers 503 until it exists, then reads it by name.
//
// # Why generate rather than require
//
// The alternative is requiring an operator to create the Secret before the
// facade will serve, which turns an optional feature into a hard
// prerequisite on every install path. Prowlarr, Sonarr and Radarr all
// generate their API key on first start for the same reason. The operator
// reads it back with
//
//	kubectl -n <ns> get secret <name> -o jsonpath='{.data.apikey}' | base64 -d
//
// # What it never does
//
// It never modifies an existing Secret. One that exists with no non-blank
// entry is left as it is: an operator who emptied it meant something, and
// the facade stays closed (503) until they fill it. It never logs a key.
func EnsureFacadeKey(ctx context.Context, c client.Client, namespace, name string) error {
	if namespace == "" || name == "" {
		return errors.New("index: the facade API-key Secret needs a namespace and a name")
	}
	key := types.NamespacedName{Namespace: namespace, Name: name}
	var sec corev1.Secret
	switch err := c.Get(ctx, key, &sec); {
	case err == nil:
		return nil
	case !apierrors.IsNotFound(err):
		return fmt.Errorf("index: read the facade API-key Secret %s: %w", key, err)
	}
	generated, err := newFacadeAPIKey()
	if err != nil {
		return err
	}
	sec = corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"app.kubernetes.io/component": facadekey.SecretComponent},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{facadekey.APIKeyField: []byte(generated)},
	}
	switch err := c.Create(ctx, &sec, client.FieldOwner(k8s.ManagerIndex.String())); {
	case err == nil:
		logging.FromContext(ctx).Info("index: generated the Torznab facade's API key; read it from the Secret",
			"secret", key.String(), "field", facadekey.APIKeyField)
		return nil
	case apierrors.IsAlreadyExists(err):
		// An operator created it between the Get and the Create: theirs wins.
		return nil
	default:
		return fmt.Errorf("index: create the facade API-key Secret %s: %w", key, err)
	}
}

// facadeKeyRunnable is EnsureFacadeKey as the manager adds it: leader-only,
// once, retried every facadeKeyRetry until it lands or the leader stops --
// a blip must not stop the manager, and the agent's facade stays closed
// meanwhile.
func facadeKeyRunnable(c client.Client, namespace, name string) k8s.LeaderOnly {
	return func(ctx context.Context) error {
		for {
			err := EnsureFacadeKey(ctx, c, namespace, name)
			if err == nil {
				return nil
			}
			logging.FromContext(ctx).Warn("index: ensuring the facade API-key Secret failed; retrying", "err", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(facadeKeyRetry):
			}
		}
	}
}

// newFacadeAPIKey returns facadeAPIKeyBytes of crypto/rand, hex-encoded.
func newFacadeAPIKey() (string, error) {
	b := make([]byte, facadeAPIKeyBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("index: generate the facade API key: %w", err)
	}
	return hex.EncodeToString(b), nil
}
