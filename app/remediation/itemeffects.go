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

package remediation

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/app/indexer/limits"
	"github.com/mediactl/clustarr/app/intake"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// The item effect kinds (ADR-0019 A3.3).
const (
	EffectFinalizer        EffectKind = "finalizer"
	EffectDispatchPublish  EffectKind = "dispatchPublish"
	EffectDispatchAnswered EffectKind = "dispatchAnswered"
	EffectRPC              EffectKind = "rpc"
	EffectSafeRemove       EffectKind = "safeRemove"
	EffectMediaFileSpec    EffectKind = "mediaFileSpec"
	EffectDelete           EffectKind = "delete"
	EffectSettleCandidate  EffectKind = "settleCandidate"
	EffectCountGrab        EffectKind = "countGrab"
	EffectApply            EffectKind = "apply"
)

// safeRemoveTimeout bounds one payload removal on the I/O pool: a removal
// walks a whole transfer's tree.
const safeRemoveTimeout = 2 * time.Minute

// EnsureFinalizer adds Name to the item, under the manager client's field
// owner clustarr (k8s.DefaultFieldOwner, k8s.EnsureFinalizer).
type EnsureFinalizer struct{ Name string }

// RemoveFinalizer drops Name from the item.
type RemoveFinalizer struct{ Name string }

// DispatchPublish is a Publish whose success confirms the ledger's
// reservation (app/dispatch: Published).
type DispatchPublish struct {
	Publish
	Durable string
	Ledger  dispatch.Key
	Seq     int64
}

// DispatchAnswered tells the ledger a dispatch's answer was incorporated.
type DispatchAnswered struct {
	Durable string
	Ledger  dispatch.Key
	Seq     int64
}

// RPC is one request and reply, bounded by Timeout; Done records the
// outcome into a leader-local book (ruling R9), on success and failure
// alike.
type RPC struct {
	Subject string
	Request any
	Reply   any
	Timeout time.Duration
	Done    func(reply any, err error)
}

// SafeRemove removes Path under the data root (fsops.SafeRemove) on the
// I/O pool. A missing path is success: a lost removal book removes again.
// Done, when set, is told the outcome (the downloads stage's removal book).
type SafeRemove struct {
	Path string
	Done func(err error)
}

// ApplyMediaFileSpec applies a placed file's MediaFile spec under
// importarr-worker (the import materialisation, A3.8): RV "" creates.
type ApplyMediaFileSpec struct {
	Namespace, Name, RV string
	Ref                 commonv1.MediaRef
	Path                string
	SizeBytes           int64
	ModTime             time.Time
	Frozen              mediafilespec.Frozen
}

// DeleteObject deletes Object with a UID precondition (and, when RV is set,
// a resourceVersion one). A NotFound or a failed precondition is success:
// the object it named is gone.
type DeleteObject struct {
	Object client.Object
	UID    types.UID
	RV     string
}

// SettleCandidate reports a parked candidate's outcome to the intake inbox,
// which acks the message (§8.4).
type SettleCandidate struct {
	MsgID   string
	Outcome intake.Outcome
}

// CountGrab counts a direct-source grab into its Indexer's grab ring
// (limits.CountGrabAt), keyed by Key -- the entry uid -- so a pass that
// counts it again counts nothing (§6.11).
type CountGrab struct {
	Namespace, IndexerRef, Key string
	At                         time.Time
}

// ApplyObject applies one object a pass owes through Apply, which renders
// the manager's complete set (an AudioGraft under importarr-worker until
// F7.1, R26). Desc names it in an error.
type ApplyObject struct {
	Desc  string
	Apply func(ctx context.Context, c client.Client) error
}

// Kind implements Effect.
func (ApplyObject) Kind() EffectKind { return EffectApply }

// Kind implements Effect.
func (EnsureFinalizer) Kind() EffectKind { return EffectFinalizer }

// Kind implements Effect.
func (RemoveFinalizer) Kind() EffectKind { return EffectFinalizer }

// Kind implements Effect.
func (DispatchPublish) Kind() EffectKind { return EffectDispatchPublish }

// Kind implements Effect.
func (DispatchAnswered) Kind() EffectKind { return EffectDispatchAnswered }

// Kind implements Effect.
func (RPC) Kind() EffectKind { return EffectRPC }

// Kind implements Effect.
func (SafeRemove) Kind() EffectKind { return EffectSafeRemove }

// Kind implements Effect.
func (ApplyMediaFileSpec) Kind() EffectKind { return EffectMediaFileSpec }

// Kind implements Effect.
func (DeleteObject) Kind() EffectKind { return EffectDelete }

// Kind implements Effect.
func (SettleCandidate) Kind() EffectKind { return EffectSettleCandidate }

// Kind implements Effect.
func (CountGrab) Kind() EffectKind { return EffectCountGrab }

// SpecWriter applies a placed file's MediaFile spec (specwrite.ApplyFacts
// in the manager); the item path holds it so this package imports no
// import-domain writer.
type SpecWriter func(ctx context.Context, c client.Client, e ApplyMediaFileSpec) error

// Run implements SafeRemove on the executor's pool.
func (x *IOExecutor) Run(ctx context.Context, op string, timeout time.Duration, call func() error) error {
	return x.run(ctx, op, timeout, call)
}

// runItemEffect runs one effect of an item pass against item.
func (ir *ItemReconciler) runItemEffect(ctx context.Context, item client.Object, eff Effect) error {
	switch e := eff.(type) {
	case Publish, History:
		return ir.runBusEffect(ctx, eff)
	case DispatchPublish:
		if err := ir.runBusEffect(ctx, e.Publish); err != nil {
			return err
		}
		if ir.Dispatch != nil {
			ir.Dispatch.Published(e.Durable, e.Ledger, e.Seq)
		}
		return nil
	case DispatchAnswered:
		if ir.Dispatch != nil {
			ir.Dispatch.Answered(e.Durable, e.Ledger, e.Seq)
		}
		return nil
	case EnsureFinalizer:
		if item == nil {
			return nil
		}
		if _, err := k8s.EnsureFinalizer(ctx, ir.Client, item, e.Name); err != nil {
			return Transient(err)
		}
		return nil
	case RemoveFinalizer:
		if item == nil {
			return nil
		}
		if _, err := k8s.RemoveFinalizer(ctx, ir.Client, item, e.Name); err != nil {
			return Transient(err)
		}
		return nil
	case RPC:
		timeout := e.Timeout
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		rctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		err := ir.Env.Bus.Request(rctx, e.Subject, e.Request, e.Reply)
		if e.Done != nil {
			e.Done(e.Reply, err)
		}
		if err != nil {
			return Transient(fmt.Errorf("rpc %s: %w", e.Subject, err))
		}
		return nil
	case SafeRemove:
		if e.Path == "" {
			return nil
		}
		root := ir.Env.DataDir
		err := ir.Env.IO.Run(ctx, "remove", safeRemoveTimeout, func() error {
			return fsops.SafeRemove(ctx, root, e.Path)
		})
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
		if e.Done != nil {
			e.Done(err)
		}
		switch {
		case err == nil:
			return nil
		case errors.Is(err, fsops.ErrOutsideRoot):
			return err
		default:
			return Transient(fmt.Errorf("remove %s: %w", e.Path, err))
		}
	case ApplyMediaFileSpec:
		if ir.SpecWriter == nil {
			return errors.New("remediation: no MediaFile spec writer is configured")
		}
		if err := ir.SpecWriter(ctx, ir.Client, e); err != nil {
			if apierrors.IsConflict(err) {
				return Transient(err)
			}
			return err
		}
		return nil
	case DeleteObject:
		if e.Object == nil {
			return nil
		}
		pre := metav1Preconditions(e.UID, e.RV)
		err := ir.Client.Delete(ctx, e.Object, client.Preconditions(pre))
		switch {
		case err == nil, apierrors.IsNotFound(err), apierrors.IsConflict(err):
			// A Conflict on a delete is a failed precondition: the object the
			// pass named is no longer the one there, which it never deletes.
			return nil
		default:
			return Transient(fmt.Errorf("delete %s/%s: %w", e.Object.GetNamespace(), e.Object.GetName(), err))
		}
	case ApplyObject:
		if e.Apply == nil {
			return nil
		}
		if err := e.Apply(ctx, ir.Client); err != nil {
			return Transient(fmt.Errorf("apply %s: %w", e.Desc, err))
		}
		return nil
	case SettleCandidate:
		if ir.Inbox != nil {
			ir.Inbox.Settle(e.MsgID, e.Outcome)
		}
		return nil
	case CountGrab:
		var idx indexv1alpha1.Indexer
		if err := ir.Env.Reader.Get(ctx, types.NamespacedName{Namespace: e.Namespace, Name: e.IndexerRef}, &idx); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return Transient(err)
		}
		if _, err := limits.CountGrabAt(ctx, ir.Env.Bus.KV(events.BucketIndexerLimits), &idx, e.Key, e.At, time.Now()); err != nil {
			return Transient(err)
		}
		return nil
	}
	return fmt.Errorf("unknown item effect %T", eff)
}

// metav1Preconditions is a delete's UID (and resourceVersion) precondition.
func metav1Preconditions(uid types.UID, rv string) metav1.Preconditions {
	pre := metav1.Preconditions{}
	if uid != "" {
		pre.UID = &uid
	}
	if rv != "" {
		pre.ResourceVersion = &rv
	}
	return pre
}

// runBusEffect publishes a Publish or a History as the file path does.
func (ir *ItemReconciler) runBusEffect(ctx context.Context, eff Effect) error {
	switch e := eff.(type) {
	case Publish:
		opts := []events.PublishOption{events.WithMsgID(e.MsgID)}
		if e.ExpectStream != "" {
			opts = append(opts, events.WithExpectStream(e.ExpectStream))
		}
		if !e.At.IsZero() {
			opts = append(opts, events.WithScheduleAt(e.At))
		}
		if _, err := ir.Env.Bus.Publish(ctx, e.Subject, e.Envelope, opts...); err != nil {
			return Transient(fmt.Errorf("publish %s: %w", e.Subject, err))
		}
		return nil
	case History:
		if _, err := ir.Env.Bus.Publish(ctx, e.Subject, e.Envelope, events.WithMsgID(e.MsgID)); err != nil {
			return Transient(fmt.Errorf("history %s: %w", e.Subject, err))
		}
		return nil
	}
	return fmt.Errorf("not a bus effect: %T", eff)
}
