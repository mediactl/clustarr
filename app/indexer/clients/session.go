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

package clients

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// The owned session Secret's keys.
const (
	// SessionSecretKeySession is the whole persisted cardigann.Session.
	SessionSecretKeySession = "session"

	// SessionSecretKeyCookie is the session's cookies as one Cookie header
	// value. app/indexer/download's generic fetcher already reads exactly this
	// key from status.sessionSecretRef, so a plain fetch of a
	// definition-backed release carries the login too.
	SessionSecretKeyCookie = "cookie"
)

// SessionKey is an Indexer's key in the clustarr-indexer-sessions bucket:
// the design spec's `<indexer-uid>`, spelled through events.KVKeyToken so the
// key grammar is enforced by construction rather than by the UID happening
// to be a UUID. pkg/events/natsbus's contract test holds it to a real server.
func SessionKey(uid types.UID) string { return events.KVKeyToken(string(uid)) }

// SessionStore persists Cardigann login sessions: into the
// clustarr-indexer-sessions KV bucket (design spec §5's KV table, 30-day TTL)
// and mirrored into an owned Secret named by status.sessionSecretRef.
//
// Why both. The KV bucket is the design's session store. The Secret is what
// survives a NATS wipe, what `kubectl` can inspect and delete to force a
// re-login, and what app/indexer/download's generic fetcher reads. Load prefers
// KV and falls back to the Secret, so a process wired without a bus still
// logs in once and reuses the session.
//
// A nil KV is "Secret only", never a failure.
type SessionStore struct {
	Client client.Client
	KV     events.KV

	// Manager is the field manager the owned Secret is applied under:
	// k8s.ManagerIndexarr in the manager (the Indexer reconciler's logins) and
	// k8s.ManagerIndexarrWorker in the index agent (a cached client's relogin
	// Save and its Drop). Both apply sessionSecretAC's one complete
	// declaration, so neither can release a field the other set: the only
	// deliberate co-ownership of the manager/agent split (§5.3.2). Empty
	// refuses every Secret write (k8s.FieldManager.Validate).
	Manager k8s.FieldManager
}

// NewSessionStore returns a store over c that writes the Secret as manager
// and, when bus is non-nil, mirrors into its clustarr-indexer-sessions bucket.
func NewSessionStore(c client.Client, bus events.Bus, manager k8s.FieldManager) *SessionStore {
	s := &SessionStore{Client: c, Manager: manager}
	if bus != nil {
		s.KV = bus.KV(events.BucketIndexerSessions)
	}
	return s
}

// Load returns idx's persisted session, or nil when it has none. A session
// the Secret holds for a DIFFERENT Indexer UID -- an Indexer deleted and
// recreated under the same name before garbage collection removed the old
// Secret -- is not this Indexer's and is ignored.
func (s *SessionStore) Load(ctx context.Context, idx *indexv1alpha1.Indexer) (*cardigann.Session, error) {
	if s == nil {
		return nil, nil
	}
	if s.KV != nil {
		e, err := s.KV.Get(ctx, SessionKey(idx.UID))
		switch {
		case err == nil:
			if sess, derr := cardigann.UnmarshalSession(e.Value); derr == nil {
				return sess, nil
			}
			// An undecodable entry falls through to the Secret rather
			// than failing: the next login overwrites both.
		case errors.Is(err, events.ErrKeyNotFound):
		default:
			// Not fatal: the Secret is the mirror, and a KV outage must
			// not fail every search on a definition-backed indexer.
			logging.FromContext(ctx).Warn("indexer: reading the session from KV failed; using the Secret",
				"indexer", client.ObjectKeyFromObject(idx), "error", err)
		}
	}
	if s.Client == nil {
		return nil, nil
	}
	var sec corev1.Secret
	key := types.NamespacedName{Namespace: idx.Namespace, Name: SessionSecretName(idx.Name)}
	if err := s.Client.Get(ctx, key, &sec); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("indexer: read session secret %s: %w", key, err)
	}
	if !ownedBy(sec.OwnerReferences, idx.UID) {
		return nil, nil
	}
	raw := sec.Data[SessionSecretKeySession]
	if len(raw) == 0 {
		return nil, nil
	}
	sess, err := cardigann.UnmarshalSession(raw)
	if err != nil {
		return nil, nil // treated as absent; the reconciler logs in again
	}
	return sess, nil
}

func ownedBy(refs []metav1.OwnerReference, uid types.UID) bool {
	for _, r := range refs {
		if r.UID == uid {
			return true
		}
	}
	return false
}

// Save writes sess to the Secret and then to KV.
//
// The Secret is applied under s.Manager with a CONTROLLER owner
// reference to the Indexer, so deleting the Indexer garbage-collects its
// session; and it is a server-side apply of both keys every time, so the
// Secret is a complete declaration and a stale cookie key can never outlive
// the session it came from.
//
// The Secret goes first because it is the durable copy: a KV write that
// failed after it leaves the next Load reading the Secret, which is correct;
// the reverse order could leave a session only in a bucket with a TTL.
func (s *SessionStore) Save(ctx context.Context, idx *indexv1alpha1.Indexer, sess *cardigann.Session) error {
	data, err := cardigann.MarshalSession(sess)
	if err != nil {
		return err
	}
	if s.Client != nil {
		ac := sessionSecretAC(idx, map[string][]byte{
			SessionSecretKeySession: data,
			SessionSecretKeyCookie:  []byte(sess.CookieHeader()),
		})
		if _, err := k8s.Apply(ctx, s.Client, s.Manager, ac); err != nil {
			return fmt.Errorf("indexer: write session secret: %w", err)
		}
	}
	if s.KV != nil {
		if _, err := s.KV.Put(ctx, SessionKey(idx.UID), data); err != nil {
			return fmt.Errorf("indexer: write session to %s: %w", events.BucketIndexerSessions, err)
		}
	}
	return nil
}

// Drop forgets idx's session, but only while the stored session is still
// failed: the one a cached client was using when the tracker killed it and
// its relogin failed. A session saved since then (the manager's login, in
// another process since the split) is left alone in the Secret and in KV
// (§5.3.2). A nil failed drops nothing: a client that had no session has
// nothing of its own to invalidate.
//
// Load reads an emptied Secret and an absent KV key as "no session", so the
// next Indexer reconcile logs in again rather than keep reusing a session the
// tracker has already killed.
//
// The Secret is emptied rather than deleted, under s.Manager, with the same
// complete declaration Save makes, so this needs no verb Save lacks. The
// Secret goes first and KV second, the order Save writes them. A search that
// re-logged in and still failed calls this; see CardigannClient.Search.
func (s *SessionStore) Drop(ctx context.Context, idx *indexv1alpha1.Indexer, failed *cardigann.Session) error {
	if s == nil || failed == nil {
		return nil
	}
	want, err := cardigann.MarshalSession(failed)
	if err != nil {
		return err
	}
	var errs []error
	if s.Client != nil {
		if err := s.dropSecret(ctx, idx, want); err != nil {
			errs = append(errs, err)
		}
	}
	if s.KV != nil {
		if err := s.dropKV(ctx, idx, want); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// dropSecret is §5.3.2 steps 1-3. It does a live read (Secrets are never
// cached), returns without writing unless data.session is still want, and
// otherwise applies the emptied declaration at the read's resourceVersion.
// A Conflict means someone wrote in between, so the whole step runs again
// from a fresh read.
func (s *SessionStore) dropSecret(ctx context.Context, idx *indexv1alpha1.Indexer, want []byte) error {
	key := types.NamespacedName{Namespace: idx.Namespace, Name: SessionSecretName(idx.Name)}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var sec corev1.Secret
		if err := s.Client.Get(ctx, key, &sec); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		if !ownedBy(sec.OwnerReferences, idx.UID) || !sameSession(sec.Data[SessionSecretKeySession], want) {
			return nil // a newer session, or not this Indexer's: nothing of ours to drop
		}
		ac := sessionSecretAC(idx, map[string][]byte{
			SessionSecretKeySession: {},
			SessionSecretKeyCookie:  {},
		}).WithResourceVersion(sec.ResourceVersion)
		_, err := k8s.Apply(ctx, s.Client, s.Manager, ac)
		return err
	})
	if err != nil {
		return fmt.Errorf("indexer: empty session secret %s: %w", key, err)
	}
	return nil
}

// dropKV is §5.3.2 step 4. A revision mismatch means a newer session was put
// after the read, and that session stays.
func (s *SessionStore) dropKV(ctx context.Context, idx *indexv1alpha1.Indexer, want []byte) error {
	key := SessionKey(idx.UID)
	e, err := s.KV.Get(ctx, key)
	switch {
	case errors.Is(err, events.ErrKeyNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("indexer: read session from %s: %w", events.BucketIndexerSessions, err)
	}
	if !sameSession(e.Value, want) {
		return nil
	}
	if err := s.KV.DeleteRevision(ctx, key, e.Revision); err != nil && !errors.Is(err, events.ErrRevisionMismatch) {
		return fmt.Errorf("indexer: drop session from %s: %w", events.BucketIndexerSessions, err)
	}
	return nil
}

// sameSession reports whether raw holds the session that marshals to want.
// raw is decoded and re-encoded first, so two encodings of one session
// compare equal. Empty or undecodable bytes never match.
func sameSession(raw, want []byte) bool {
	got, err := cardigann.UnmarshalSession(raw)
	if err != nil {
		return false
	}
	norm, err := cardigann.MarshalSession(got)
	return err == nil && bytes.Equal(norm, want)
}

// sessionSecretAC is the owned session Secret's complete declaration: a
// CONTROLLER owner reference to idx, type Opaque, and data. Save and Drop
// both render it here and apply it under s.Manager, so the two cannot declare
// different field sets under one manager, and the manager's and the index
// agent's stores (two managers since the split) declare the same set.
func sessionSecretAC(idx *indexv1alpha1.Indexer, data map[string][]byte) *corev1ac.SecretApplyConfiguration {
	owner := metav1ac.OwnerReference().
		WithAPIVersion(indexv1alpha1.GroupVersion.String()).
		WithKind("Indexer").
		WithName(idx.Name).
		WithUID(idx.UID).
		WithController(true).
		WithBlockOwnerDeletion(true)
	return corev1ac.Secret(SessionSecretName(idx.Name), idx.Namespace).
		WithOwnerReferences(owner).
		WithType(corev1.SecretTypeOpaque).
		WithData(data)
}
