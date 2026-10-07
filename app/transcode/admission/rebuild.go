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

package admission

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// RebuildInputs is what a rebuild reads.
type RebuildInputs struct {
	APIReader  client.Reader // quorum reads
	Cache      client.Reader // the manager cache: the backlog scan
	Namespaces []string      // empty: all
	// Eligible is the backlog scan's test (§5.6 step 3); nil scans nothing.
	Eligible func(ctx context.Context, mf *catalogv1alpha1.MediaFile) *transcodeplan.Eligibility
}

var windowPhases = []catalogv1alpha1.TranscodePhase{
	catalogv1alpha1.TranscodePhasePending, catalogv1alpha1.TranscodePhasePlanned,
	catalogv1alpha1.TranscodePhaseQueued, catalogv1alpha1.TranscodePhaseRunning, catalogv1alpha1.TranscodePhaseSwapping,
}

// Rebuild is the first pass's quorum read (§5.6): seven server-filtered
// Lists through the APIReader on the selectable status.transcode.phase and
// status.graft.phase (five transcode window phases, two graft phases), then
// the backlog scan of the cache, then rebuilt -- no grant is issued before
// it. The cache may not yet hold the previous leader's last writes when the
// lease changes hands; the apiserver does (D6).
func (l *Ledger) Rebuild(ctx context.Context, in RebuildInputs) error {
	return l.rebuild(ctx, in, false)
}

// Resync repeats the rebuild every ledgerResync, replacing rather than
// merging what it reads: an entry observed before the Lists started takes
// their view outright, an in-flight or window entry they did not return is
// re-read and replaced (or forgotten), and every correction is counted and
// logged (§5.6 "Resync").
func (l *Ledger) Resync(ctx context.Context, in RebuildInputs) error {
	return l.rebuild(ctx, in, true)
}

func (l *Ledger) rebuild(ctx context.Context, in RebuildInputs, replace bool) error {
	started := l.clock.Now()
	seen := map[types.UID]bool{}
	list := func(ns, field, value string) error {
		var list catalogv1alpha1.MediaFileList
		opts := []client.ListOption{client.MatchingFields{field: value}}
		if ns != "" {
			opts = append(opts, client.InNamespace(ns))
		}
		if err := in.APIReader.List(ctx, &list, opts...); err != nil {
			return fmt.Errorf("admission: rebuild: list MediaFiles in %q with %s=%s: %w", ns, field, value, err)
		}
		for i := range list.Items {
			mf := &list.Items[i]
			seen[mf.UID] = true
			l.observeQuorum(ctx, FileViewOf(mf, &mf.Status, nil), started, replace)
		}
		return nil
	}
	for _, ns := range namespacesOrAll(in.Namespaces) {
		for _, p := range windowPhases {
			if err := list(ns, catalogv1alpha1.FieldTranscodePhase, string(p)); err != nil {
				return err
			}
		}
		for _, p := range []catalogv1alpha1.GraftPhase{catalogv1alpha1.GraftPhaseQueued, catalogv1alpha1.GraftPhaseRunning} {
			if err := list(ns, catalogv1alpha1.FieldGraftPhase, string(p)); err != nil {
				return err
			}
		}
	}
	if replace {
		if err := l.correctUnseen(ctx, in.APIReader, seen, started); err != nil {
			return err
		}
	}
	if err := l.scanBacklog(ctx, in); err != nil {
		return err
	}
	l.markRebuilt()
	return nil
}

// namespacesOrAll is ns, or the one all-namespaces "" when empty.
func namespacesOrAll(ns []string) []string {
	if len(ns) == 0 {
		return []string{""}
	}
	return ns
}

// observeQuorum observes one quorum view. In a resync an entry observed
// before the Lists started takes it outright (a newer loop Observe stands),
// and any change is a correction.
func (l *Ledger) observeQuorum(ctx context.Context, v FileView, started time.Time, replace bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	cur := l.files[v.UID]
	if !replace || cur == nil || !cur.observedAt.Before(started) {
		l.observeLocked(v, now)
		return
	}
	if cur.view.Phase != v.Phase || cur.view.Seq != v.Seq || cur.view.Class != v.Class {
		metrics.TranscodeLedgerCorrections.WithLabelValues().Inc()
		logging.FromContext(ctx).Warn("admission: resync corrected an entry the ledger had missed",
			"mediafile", v.Key.String(), "had", string(cur.view.Phase), "hadSeq", cur.view.Seq,
			"now", string(v.Phase), "seq", v.Seq)
	}
	l.storeLocked(v, cur, now)
}

// correctUnseen re-reads every in-flight or window entry the resync's Lists
// did not return and was observed before they started: replaced from that
// read, or forgotten on NotFound.
func (l *Ledger) correctUnseen(ctx context.Context, api client.Reader, seen map[types.UID]bool, started time.Time) error {
	l.mu.Lock()
	var stale []FileView
	for uid, e := range l.files {
		if !seen[uid] && windowPhase(e.view.Phase) && e.observedAt.Before(started) {
			stale = append(stale, e.view)
		}
	}
	l.mu.Unlock()
	for _, v := range stale {
		var mf catalogv1alpha1.MediaFile
		switch err := api.Get(ctx, v.Key, &mf); {
		case apierrors.IsNotFound(err):
			metrics.TranscodeLedgerCorrections.WithLabelValues().Inc()
			l.Forget(v.Key)
		case err != nil:
			return fmt.Errorf("admission: resync: get MediaFile %s: %w", v.Key, err)
		default:
			l.observeQuorum(ctx, FileViewOf(&mf, &mf.Status, nil), started, true)
		}
	}
	return nil
}

// scanBacklog is §5.6 step 3: every movie and episode file in the cache,
// through in.Eligible, once -- the backlog the window fills from.
func (l *Ledger) scanBacklog(ctx context.Context, in RebuildInputs) error {
	if in.Eligible == nil || in.Cache == nil {
		return nil
	}
	for _, ns := range namespacesOrAll(in.Namespaces) {
		var list catalogv1alpha1.MediaFileList
		opts := []client.ListOption{client.UnsafeDisableDeepCopy}
		if ns != "" {
			opts = append(opts, client.InNamespace(ns))
		}
		if err := in.Cache.List(ctx, &list, opts...); err != nil {
			return fmt.Errorf("admission: rebuild: list cached MediaFiles: %w", err)
		}
		now := l.clock.Now()
		for i := range list.Items {
			mf := &list.Items[i]
			if !transcodeplan.EligibleKind(mf.Spec.MediaRef.Kind) {
				continue
			}
			e := in.Eligible(ctx, mf)
			if e == nil {
				continue
			}
			l.mu.Lock()
			l.observeLocked(FileViewOf(mf, &mf.Status, e), now)
			l.mu.Unlock()
		}
	}
	return nil
}
