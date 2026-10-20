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

package iptvprovider

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/tuner"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// KeptSecretKey is the data key of a provider's kept playlist: gzip JSON of
// a tuner.KeptPlaylist.
const KeptSecretKey = "playlist.json.gz"

// maxKeptBytes keeps the kept playlist under the apiserver's 1 MiB Secret
// limit; a larger one is not kept, and a restart waits for a fetch as before.
const maxKeptBytes = 900 << 10

// KeptSecretName is the Secret holding provider's kept playlist, owned by
// the provider, written only by this controller.
func KeptSecretName(provider string) string { return provider + "-livetv-playlist" }

// keptState is what this process knows of each provider's kept Secret.
type keptState struct {
	mu sync.Mutex
	// read marks a provider whose Secret this process has read (or found
	// absent): it is read once, at the first reconcile after a start.
	read map[types.NamespacedName]bool
	// digest is the kept playlist the Secret holds, so an unchanged one is
	// never written again.
	digest map[types.NamespacedName]string
	// foreign marks a Secret of that name that this provider does not own:
	// it is neither read nor written.
	foreign map[types.NamespacedName]bool
}

func (s *keptState) forget(key types.NamespacedName) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.read, key)
	delete(s.digest, key)
	delete(s.foreign, key)
}

func (s *keptState) record(key types.NamespacedName, digest string, foreign bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.read == nil {
		s.read, s.digest, s.foreign = map[types.NamespacedName]bool{}, map[types.NamespacedName]string{}, map[types.NamespacedName]bool{}
	}
	s.read[key], s.digest[key], s.foreign[key] = true, digest, foreign
}

func (s *keptState) get(key types.NamespacedName) (read bool, digest string, foreign bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read[key], s.digest[key], s.foreign[key]
}

// restoreKept reads p's kept playlist once per process and hands it to the
// tuner while the tuner has fetched nothing, so a restart while the provider
// cannot be fetched still tunes its channels. A Secret that is absent, not
// p's, or unreadable restores nothing; the next fetch rewrites it.
func (r *Reconciler) restoreKept(ctx context.Context, p *clustarrv1.IPTVProvider) error {
	key := types.NamespacedName{Namespace: p.Namespace, Name: p.Name}
	if read, _, _ := r.kept.get(key); read {
		return nil
	}
	var s corev1.Secret
	err := r.SecretReader.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: KeptSecretName(p.Name)}, &s)
	switch {
	case apierrors.IsNotFound(err):
		r.kept.record(key, "", false)
		return nil
	case err != nil:
		return fmt.Errorf("livetv: read kept playlist: %w", err)
	case !ownedBy(&s, p):
		logging.FromContext(ctx).Warn("livetv: a Secret of the kept playlist's name is not the provider's; not keeping one",
			"provider", key.String(), "secret", s.Name)
		r.kept.record(key, "", true)
		return nil
	}
	k, err := decodeKept(s.Data[KeptSecretKey])
	if err != nil {
		logging.FromContext(ctx).Warn("livetv: kept playlist unreadable", "provider", key.String(), "err", err)
		r.kept.record(key, "", false)
		return nil
	}
	r.kept.record(key, k.Digest(), false)
	if snap, ok := r.Tuner.Snapshot(key); !ok || !snap.PlaylistFetched {
		r.Tuner.Restore(key, k)
	}
	return nil
}

// keepPlaylist writes the tuner's kept playlist into p's Secret whenever it
// differs from what the Secret holds.
func (r *Reconciler) keepPlaylist(ctx context.Context, p *clustarrv1.IPTVProvider) error {
	key := types.NamespacedName{Namespace: p.Namespace, Name: p.Name}
	read, held, foreign := r.kept.get(key)
	if !read || foreign || p.UID == "" {
		return nil
	}
	k, ok := r.Tuner.Kept(key)
	if !ok {
		return nil
	}
	digest := k.Digest()
	if digest == held {
		return nil
	}
	data, err := encodeKept(k)
	if err != nil {
		return err
	}
	if len(data) > maxKeptBytes {
		logging.FromContext(ctx).Warn("livetv: kept playlist too large for a Secret; a restart waits for a fetch",
			"provider", key.String(), "bytes", len(data), "channels", len(k.Channels))
		r.kept.record(key, digest, false)
		return nil
	}
	owner := metav1ac.OwnerReference().
		WithAPIVersion(clustarrv1.GroupVersion.String()).
		WithKind("IPTVProvider").
		WithName(p.Name).
		WithUID(p.UID).
		WithController(true).
		WithBlockOwnerDeletion(true)
	ac := corev1ac.Secret(KeptSecretName(p.Name), p.Namespace).
		WithOwnerReferences(owner).
		WithType(corev1.SecretTypeOpaque).
		WithData(map[string][]byte{KeptSecretKey: data})
	if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerLiveTV, ac); err != nil {
		return fmt.Errorf("livetv: keep playlist: %w", err)
	}
	r.kept.record(key, digest, false)
	return nil
}

func ownedBy(s *corev1.Secret, p *clustarrv1.IPTVProvider) bool {
	for _, o := range s.OwnerReferences {
		if o.UID == p.UID {
			return true
		}
	}
	return false
}

func encodeKept(k tuner.KeptPlaylist) ([]byte, error) {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	if err := json.NewEncoder(zw).Encode(k); err != nil {
		return nil, fmt.Errorf("livetv: encode kept playlist: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("livetv: encode kept playlist: %w", err)
	}
	return b.Bytes(), nil
}

func decodeKept(data []byte) (tuner.KeptPlaylist, error) {
	var k tuner.KeptPlaylist
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return k, err
	}
	defer func() { _ = zr.Close() }()
	if err := json.NewDecoder(io.LimitReader(zr, 16<<20)).Decode(&k); err != nil {
		return k, err
	}
	return k, nil
}
