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
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/events"
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

// SessionStore keeps Cardigann login sessions in the
// clustarr-indexer-sessions KV bucket (design spec §5's KV table, 30-day
// TTL), and reads the owned Secret named by status.sessionSecretRef as the
// fallback.
//
// It WRITES only the KV (ADR-0019 §7.5, W32, W33). The Secret is the
// Indexer controller's alone: it mirrors the KV session into it under
// k8s.ManagerIndex (app/indexer/controller/indexer's sessionsecret.go), so
// the index agent, which builds this store for its relogins, links no
// Secret writer. The Secret is still what survives a NATS wipe, what
// `kubectl` can inspect and delete to force a re-login, and what
// app/indexer/download's generic fetcher reads, so Load prefers KV and falls
// back to it.
//
// A nil KV writes nothing and reads the Secret alone; a nil Reader reads the
// KV alone.
type SessionStore struct {
	// Reader reads the owned Secret. Secrets are never cached, so it is a
	// live Get by name.
	Reader client.Reader
	KV     events.KV
}

// NewSessionStore returns a store that reads the Secret through r and, when
// bus is non-nil, keeps sessions in its clustarr-indexer-sessions bucket.
func NewSessionStore(r client.Reader, bus events.Bus) *SessionStore {
	s := &SessionStore{Reader: r}
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
	if s.Reader == nil {
		return nil, nil
	}
	var sec corev1.Secret
	key := types.NamespacedName{Namespace: idx.Namespace, Name: SessionSecretName(idx.Name)}
	if err := s.Reader.Get(ctx, key, &sec); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("indexer: read session secret %s: %w", key, err)
	}
	if !OwnedBy(sec.OwnerReferences, idx.UID) {
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

// OwnedBy reports whether refs name uid: a session Secret left by an Indexer
// deleted and recreated under the same name is not the new one's.
func OwnedBy(refs []metav1.OwnerReference, uid types.UID) bool {
	for _, r := range refs {
		if r.UID == uid {
			return true
		}
	}
	return false
}

// Save puts sess in KV, the one copy this store writes. The Indexer
// controller mirrors it into the owned Secret when the put reaches it
// (W32); the controller's own login path mirrors at once.
func (s *SessionStore) Save(ctx context.Context, idx *indexv1alpha1.Indexer, sess *cardigann.Session) error {
	if s == nil || s.KV == nil {
		return nil
	}
	data, err := cardigann.MarshalSession(sess)
	if err != nil {
		return err
	}
	if _, err := s.KV.Put(ctx, SessionKey(idx.UID), data); err != nil {
		return fmt.Errorf("indexer: write session to %s: %w", events.BucketIndexerSessions, err)
	}
	return nil
}

// Drop forgets idx's session, but only while the stored session is still
// failed: the one a cached client was using when the tracker killed it and
// its relogin failed. A session saved since then (the manager's login) is
// left alone (§5.3.2). A nil failed drops nothing: a client that had no
// session has nothing of its own to invalidate.
//
// It deletes the KV key by revision (W33); the Indexer controller sees the
// delete, empties the owned Secret by compare-and-swap, and logs in again,
// so the next reconcile does not keep reusing a session the tracker has
// already killed. A search that re-logged in and still failed calls this;
// see CardigannClient.Search.
func (s *SessionStore) Drop(ctx context.Context, idx *indexv1alpha1.Indexer, failed *cardigann.Session) error {
	if s == nil || failed == nil || s.KV == nil {
		return nil
	}
	want, err := cardigann.MarshalSession(failed)
	if err != nil {
		return err
	}
	return s.dropKV(ctx, idx, want)
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
	if !SameSession(e.Value, want) {
		return nil
	}
	if err := s.KV.DeleteRevision(ctx, key, e.Revision); err != nil && !errors.Is(err, events.ErrRevisionMismatch) {
		return fmt.Errorf("indexer: drop session from %s: %w", events.BucketIndexerSessions, err)
	}
	return nil
}

// SameSession reports whether raw holds the session that marshals to want.
// raw is decoded and re-encoded first, so two encodings of one session
// compare equal. Empty or undecodable bytes never match.
func SameSession(raw, want []byte) bool {
	got, err := cardigann.UnmarshalSession(raw)
	if err != nil {
		return false
	}
	norm, err := cardigann.MarshalSession(got)
	return err == nil && bytes.Equal(norm, want)
}
