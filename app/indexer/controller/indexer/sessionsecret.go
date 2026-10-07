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
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	idxclients "github.com/mediactl/clustarr/app/indexer/clients"
	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// The Secret half of the session store (ADR-0019 §7.5, W32, W33). The
// SessionStore keeps sessions in clustarr-indexer-sessions only; this
// controller is the owned session Secret's one writer, under
// k8s.ManagerIndex: its own login path mirrors at once, and a put or delete
// any other writer (the index agent's relogin Save, its Drop) makes in the
// KV reaches the Secret through sessionsSource and the next reconcile.

// sessionMirror is what the next reconcile owes an Indexer's session Secret.
type sessionMirror int

const (
	// mirrorPut: copy the KV session into the Secret when they differ. It
	// is also what a leader owes an Indexer it has not mirrored yet.
	mirrorPut sessionMirror = iota
	// mirrorDrop: the KV session was deleted; empty the Secret unless the
	// KV holds a newer session again by the time the reconcile runs.
	mirrorDrop
	// mirrorDone: nothing is owed.
	mirrorDone
)

// mirrorState is an Indexer's owed mirror and how many times sessionsSource
// has marked it, so a pass settles only what it read: a put that arrives
// while a pass mirrors an older session stays owed.
type mirrorState struct {
	owed sessionMirror
	gen  uint64
}

// mirrorSession applies sess to idx's owned session Secret under
// k8s.ManagerIndex: a CONTROLLER owner reference to the Indexer, so deleting
// the Indexer garbage-collects its session, and a server-side apply of both
// keys every time, so the Secret is a complete declaration and a stale
// cookie key never outlives the session it came from.
func mirrorSession(ctx context.Context, c client.Client, idx *indexv1alpha1.Indexer, sess *cardigann.Session) error {
	data, err := cardigann.MarshalSession(sess)
	if err != nil {
		return err
	}
	ac := sessionSecretAC(idx, map[string][]byte{
		idxclients.SessionSecretKeySession: data,
		idxclients.SessionSecretKeyCookie:  []byte(sess.CookieHeader()),
	})
	if _, err := k8s.Apply(ctx, c, k8s.ManagerIndex, ac); err != nil {
		return fmt.Errorf("indexer: write session secret: %w", err)
	}
	return nil
}

// dropSessionSecret empties idx's owned session Secret by compare-and-swap:
// a live read (Secrets are never cached), no write unless the Secret is
// idx's and holds a session -- failed's, when given -- and the emptied
// declaration applied at the read's resourceVersion, redone from a fresh
// read on a Conflict. It is emptied rather than deleted, with the same
// complete declaration mirrorSession makes, so this needs no verb that one
// lacks; the SessionStore reads an emptied Secret as "no session".
func dropSessionSecret(ctx context.Context, c client.Client, idx *indexv1alpha1.Indexer, failed *cardigann.Session) error {
	var want []byte
	if failed != nil {
		var err error
		if want, err = cardigann.MarshalSession(failed); err != nil {
			return err
		}
	}
	key := types.NamespacedName{Namespace: idx.Namespace, Name: idxclients.SessionSecretName(idx.Name)}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var sec corev1.Secret
		if err := c.Get(ctx, key, &sec); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		held := sec.Data[idxclients.SessionSecretKeySession]
		switch {
		case !idxclients.OwnedBy(sec.OwnerReferences, idx.UID), len(held) == 0:
			return nil // not this Indexer's, or already empty
		case want != nil && !idxclients.SameSession(held, want):
			return nil // a newer session: nothing of ours to drop
		}
		ac := sessionSecretAC(idx, map[string][]byte{
			idxclients.SessionSecretKeySession: {},
			idxclients.SessionSecretKeyCookie:  {},
		}).WithResourceVersion(sec.ResourceVersion)
		_, err := k8s.Apply(ctx, c, k8s.ManagerIndex, ac)
		return err
	})
	if err != nil {
		return fmt.Errorf("indexer: empty session secret %s: %w", key, err)
	}
	return nil
}

// sessionSecretAC is the owned session Secret's complete declaration: a
// CONTROLLER owner reference to idx, type Opaque, and data. mirrorSession
// and dropSessionSecret both render it here, so the two cannot declare
// different field sets under one manager.
func sessionSecretAC(idx *indexv1alpha1.Indexer, data map[string][]byte) *corev1ac.SecretApplyConfiguration {
	owner := metav1ac.OwnerReference().
		WithAPIVersion(indexv1alpha1.GroupVersion.String()).
		WithKind("Indexer").
		WithName(idx.Name).
		WithUID(idx.UID).
		WithController(true).
		WithBlockOwnerDeletion(true)
	return corev1ac.Secret(idxclients.SessionSecretName(idx.Name), idx.Namespace).
		WithOwnerReferences(owner).
		WithType(corev1.SecretTypeOpaque).
		WithData(data)
}

// syncSessionSecret settles what this leader owes idx's session Secret
// (sessionsSource marks it; an Indexer this leader has not mirrored owes a
// put). A put copies the KV session into the Secret when the Secret holds
// another; a drop empties the Secret by compare-and-swap unless the KV holds
// a session again. A KV key that is merely absent empties nothing: the
// Secret is the copy that survives a NATS wipe. It runs before the login
// path reads the store, so a session the index agent dropped is not read
// back from the Secret and reused.
func (r *Reconciler) syncSessionSecret(ctx context.Context, idx *indexv1alpha1.Indexer) error {
	if r.Bus == nil || idx.UID == "" {
		return nil
	}
	owed, gen := r.owedMirror(idx.UID)
	if owed == mirrorDone {
		return nil
	}
	sess, err := r.kvSession(ctx, idx)
	if err != nil {
		return err
	}
	switch {
	case sess != nil:
		if err := r.mirrorIfDifferent(ctx, idx, sess); err != nil {
			return err
		}
	case owed == mirrorDrop:
		if err := dropSessionSecret(ctx, r.Client, idx, nil); err != nil {
			return err
		}
	}
	r.settleMirror(idx.UID, gen)
	return nil
}

// kvSession reads idx's session from clustarr-indexer-sessions, nil when
// absent or undecodable.
func (r *Reconciler) kvSession(ctx context.Context, idx *indexv1alpha1.Indexer) (*cardigann.Session, error) {
	e, err := r.Bus.KV(events.BucketIndexerSessions).Get(ctx, idxclients.SessionKey(idx.UID))
	switch {
	case errors.Is(err, events.ErrKeyNotFound):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("indexer: read the session from %s: %w", events.BucketIndexerSessions, err)
	case e.Operation != events.KVPut || len(e.Value) == 0:
		return nil, nil
	}
	sess, derr := cardigann.UnmarshalSession(e.Value)
	if derr != nil {
		logging.FromContext(ctx).Warn("indexer: the KV session does not decode; not mirroring it",
			"indexer", client.ObjectKeyFromObject(idx), "error", derr)
		return nil, nil
	}
	return sess, nil
}

// mirrorIfDifferent applies sess to the Secret unless it already holds it,
// so a pass that owes a put costs one live Get when nothing changed.
func (r *Reconciler) mirrorIfDifferent(ctx context.Context, idx *indexv1alpha1.Indexer, sess *cardigann.Session) error {
	want, err := cardigann.MarshalSession(sess)
	if err != nil {
		return err
	}
	var sec corev1.Secret
	key := types.NamespacedName{Namespace: idx.Namespace, Name: idxclients.SessionSecretName(idx.Name)}
	switch err := r.Client.Get(ctx, key, &sec); {
	case apierrors.IsNotFound(err):
	case err != nil:
		return fmt.Errorf("indexer: read session secret %s: %w", key, err)
	case idxclients.OwnedBy(sec.OwnerReferences, idx.UID) &&
		idxclients.SameSession(sec.Data[idxclients.SessionSecretKeySession], want):
		return nil
	}
	return mirrorSession(ctx, r.Client, idx, sess)
}

// owedMirror is what idx's Secret is owed, and the mark's generation; an
// Indexer this leader has not mirrored owes a put.
func (r *Reconciler) owedMirror(uid types.UID) (sessionMirror, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m, ok := r.mirrors[uid]; ok {
		return m.owed, m.gen
	}
	return mirrorPut, 0
}

// owe records what sessionsSource saw: a drop is never downgraded to a put
// before it is settled, since the pass re-reads the KV either way.
func (r *Reconciler) owe(uid types.UID, m sessionMirror) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mirrors == nil {
		r.mirrors = map[types.UID]mirrorState{}
	}
	cur := r.mirrors[uid]
	if cur.owed == mirrorDrop && m == mirrorPut {
		m = mirrorDrop
	}
	r.mirrors[uid] = mirrorState{owed: m, gen: cur.gen + 1}
}

// settleMirror marks uid's Secret as owed nothing, unless sessionsSource
// marked it again since gen was read.
func (r *Reconciler) settleMirror(uid types.UID, gen uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.mirrors == nil {
		r.mirrors = map[types.UID]mirrorState{}
	}
	if cur, ok := r.mirrors[uid]; ok && cur.gen != gen {
		return
	}
	r.mirrors[uid] = mirrorState{owed: mirrorDone, gen: gen}
}
