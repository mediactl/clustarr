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

package importlist

import (
	"context"
	"fmt"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importliststate"
	"github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/importlist/trakt"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// SecretTokenStore implements pkg/importlist.TokenStore by persisting one
// ImportList's Trakt access/refresh token pair in an owned Secret
// (importliststate.TraktTokenSecretName), so a token survives a restart the
// way importlist.MemoryTokenStore does not. It also holds the in-flight
// device code between the controller's Start and Poll calls.
//
// The ImportList controller is the Secret's only writer (ADR-0019 §7.6,
// W30): it drives the device flow and is the one refresher, so a rotated
// refresh token -- Trakt's refresh tokens are single-use -- is never lost
// to a second writer. The import agent only reads the Secret
// (importliststate.TokenReader). Every write is one apply of the Secret's
// whole data map under k8s.ManagerImport, with the read's resourceVersion:
// a compare-and-swap (spec 2026-10-06 §5.5, OD13), redone from a fresh read
// on a Conflict.
type SecretTokenStore struct {
	*importliststate.TokenReader

	// Writer applies the Secret.
	Writer client.Client
	// List is the ImportList the Secret belongs to, and its owner.
	List *catalogv1alpha1.ImportList
	// Clock stamps importliststate.TraktSecretKeyIssuedAt. Nil means
	// time.Now.
	Clock func() time.Time
}

// NewSecretTokenStore returns the token store of list's Trakt token, read
// and written through c.
func NewSecretTokenStore(c client.Client, list *catalogv1alpha1.ImportList, clock func() time.Time) *SecretTokenStore {
	return &SecretTokenStore{
		TokenReader: importliststate.NewTokenReader(c, list), Writer: c, List: list, Clock: clock,
	}
}

func (s *SecretTokenStore) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

// owner is the Secret's controller reference to its ImportList, so it is
// garbage-collected with the list.
func (s *SecretTokenStore) owner() *metav1ac.OwnerReferenceApplyConfiguration {
	return metav1ac.OwnerReference().
		WithAPIVersion(catalogv1alpha1.GroupVersion.String()).
		WithKind("ImportList").
		WithName(s.List.Name).
		WithUID(s.List.UID).
		WithController(true).
		WithBlockOwnerDeletion(true)
}

// applyAll reads the Secret, lets mutate change its data, and applies the
// WHOLE resulting map under k8s.ManagerImport with the read's
// resourceVersion. Every key this store ever writes is sent together, so no
// apply releases another's keys.
func (s *SecretTokenStore) applyAll(ctx context.Context, mutate func(map[string][]byte)) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		data, rv, err := s.Data(ctx)
		if err != nil {
			return err
		}
		next := make(map[string][]byte, len(data))
		for k, v := range data {
			next[k] = v
		}
		mutate(next)
		ac := corev1ac.Secret(importliststate.TraktTokenSecretName(s.List.Name), s.List.Namespace).
			WithOwnerReferences(s.owner()).
			WithType(corev1.SecretTypeOpaque).
			WithData(next)
		if rv != "" {
			ac = ac.WithResourceVersion(rv)
		}
		if _, err := k8s.Apply(ctx, s.Writer, k8s.ManagerImport, ac); err != nil {
			return fmt.Errorf("importlist: write trakt token secret %s: %w", s.SecretKey(), err)
		}
		return nil
	})
}

// Save implements pkg/importlist.TokenStore, stamping when the pair was
// stored. It does not clear the in-flight device-code keys: ClearDeviceCode
// does that once the controller has recorded the authorized state.
func (s *SecretTokenStore) Save(ctx context.Context, t importlist.Token) error {
	issued := s.now().UTC().Format(time.RFC3339)
	return s.applyAll(ctx, func(data map[string][]byte) {
		data[importliststate.TraktSecretKeyAccessToken] = []byte(t.AccessToken)
		data[importliststate.TraktSecretKeyRefreshToken] = []byte(t.RefreshToken)
		data[importliststate.TraktSecretKeyExpiresAt] = []byte(t.ExpiresAt.UTC().Format(time.RFC3339))
		data[importliststate.TraktSecretKeyIssuedAt] = []byte(issued)
	})
}

// ClearToken removes the stored token pair: Trakt refused it and could not
// refresh it, so the next reconcile starts a device flow instead of
// reading the dead pair as authorized.
func (s *SecretTokenStore) ClearToken(ctx context.Context) error {
	return s.applyAll(ctx, func(data map[string][]byte) {
		delete(data, importliststate.TraktSecretKeyAccessToken)
		delete(data, importliststate.TraktSecretKeyRefreshToken)
		delete(data, importliststate.TraktSecretKeyExpiresAt)
		delete(data, importliststate.TraktSecretKeyIssuedAt)
	})
}

// SaveDeviceCode records an in-flight device code so the controller can
// resume polling it across reconciles and restarts without a second Start
// call minting a code the user was never shown.
func (s *SecretTokenStore) SaveDeviceCode(ctx context.Context, dc trakt.DeviceCode) error {
	return s.applyAll(ctx, func(data map[string][]byte) {
		data[importliststate.TraktSecretKeyDeviceCode] = []byte(dc.DeviceCode)
		data[importliststate.TraktSecretKeyDeviceCodeExpiresAt] = []byte(dc.ExpiresAt.UTC().Format(time.RFC3339))
		data[importliststate.TraktSecretKeyPollIntervalSeconds] = []byte(strconv.FormatInt(int64(dc.Interval/time.Second), 10))
	})
}

// ClearDeviceCode removes the in-flight device code once it is no longer
// needed: the user approved it (Poll returned authorized, and Save has
// already written the real token) or it expired or was denied and a fresh
// Start is about to mint a new one.
func (s *SecretTokenStore) ClearDeviceCode(ctx context.Context) error {
	return s.applyAll(ctx, func(data map[string][]byte) {
		delete(data, importliststate.TraktSecretKeyDeviceCode)
		delete(data, importliststate.TraktSecretKeyDeviceCodeExpiresAt)
		delete(data, importliststate.TraktSecretKeyPollIntervalSeconds)
	})
}

var _ importlist.TokenStore = (*SecretTokenStore)(nil)
