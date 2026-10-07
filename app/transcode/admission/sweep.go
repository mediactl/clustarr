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
	"errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// InFlightOrGranted reports whether uid has a dispatch in flight (Queued,
// Running or Swapping) or a grant: its stored task is its own.
func (l *Ledger) InFlightOrGranted(uid types.UID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.grants[uid] != nil {
		return true
	}
	e := l.files[uid]
	return e != nil && (slotPhase(e.view.Phase) || e.view.Phase == catalogv1alpha1.TranscodePhaseSwapping)
}

// sweep runs at the first pass and with every resync (loop spec §5.6
// "Sweep"; the sweep a controller ran before ADR-0016, re-keyed on
// MediaFile UIDs): a
// stored task whose last token is a UID no cached MediaFile has is the task
// of a file deleted while the manager was down, and is withdrawn with
// WithdrawAll; a live file's task that is neither in flight nor granted is
// purged; then the durables of deleted profiles' pools are deleted.
func (r *Reconciler) sweep(ctx context.Context) error {
	if r.Admin == nil {
		return nil
	}
	subjects, err := r.Admin.Subjects(ctx, events.StreamWorkTranscode, "clustarr.work.transcode.task.>")
	if err != nil {
		return fmt.Errorf("admission: sweep: list task subjects: %w", err)
	}
	log := logging.FromContext(ctx)
	var errs []error
	for _, subject := range subjects {
		uid := subject[strings.LastIndex(subject, ".")+1:]
		var l catalogv1alpha1.MediaFileList
		if err := r.Client.List(ctx, &l, client.MatchingFields{mfindex.UID: uid}, client.UnsafeDisableDeepCopy); err != nil {
			errs = append(errs, err)
			continue
		}
		switch {
		case len(l.Items) == 0:
			if err := r.Withdrawal.WithdrawAll(ctx, uid); err != nil {
				errs = append(errs, err)
				continue
			}
			log.InfoContext(ctx, "admission: withdrew the task of a deleted file", "uid", uid)
		case !r.Ledger.InFlightOrGranted(types.UID(uid)):
			if err := r.Admin.PurgeSubject(ctx, events.StreamWorkTranscode, subject); err != nil {
				errs = append(errs, err)
				continue
			}
			log.InfoContext(ctx, "admission: purged a task no dispatch owns", "subject", subject)
		}
	}
	return errors.Join(append(errs, r.sweepDurables(ctx))...)
}

// sweepDurables deletes every pool durable on the transcode work stream
// whose TranscodeProfile no longer exists (final-review M1): a worker's Pull
// creates its pool's durable, and nothing else removes it or its
// dead-letter watcher. A durable is kept when it is the name
// events.TranscodeTaskConsumerName builds for an existing profile's UID and
// a pool class, and so is every durable that is not a pool's at all.
func (r *Reconciler) sweepDurables(ctx context.Context) error {
	names, err := r.Admin.Subscriptions(ctx, events.StreamWorkTranscode)
	if err != nil {
		return fmt.Errorf("admission: sweep: list the work stream's durables: %w", err)
	}
	var profiles transcodev1alpha1.TranscodeProfileList
	if err := r.Client.List(ctx, &profiles); err != nil {
		return fmt.Errorf("admission: sweep: list TranscodeProfiles: %w", err)
	}
	live := make(map[string]bool, len(profiles.Items)*len(pool.Classes))
	for i := range profiles.Items {
		for _, class := range pool.Classes {
			live[events.TranscodeTaskConsumerName(string(profiles.Items[i].UID), string(class))] = true
		}
	}
	log := logging.FromContext(ctx)
	var errs []error
	for _, name := range names {
		if live[name] || !isPoolDurable(name) {
			continue
		}
		if err := r.Admin.DeleteSubscription(ctx, events.StreamWorkTranscode, name); err != nil {
			errs = append(errs, fmt.Errorf("admission: sweep: delete the durable %s of a deleted TranscodeProfile's pool: %w", name, err))
			continue
		}
		log.InfoContext(ctx, "admission: deleted the durable of a deleted TranscodeProfile's pool", "durable", name)
	}
	return errors.Join(errs...)
}

// isPoolDurable reports whether name is a pool's durable: one
// events.TranscodeTaskConsumerName builds, and never one of the shipped
// topology.
func isPoolDurable(name string) bool {
	if _, shipped := events.Default().Consumer(name); shipped || !strings.HasPrefix(name, events.TranscodePoolDurablePrefix) {
		return false
	}
	for _, class := range pool.Classes {
		if strings.HasSuffix(name, "-"+events.KVKeyToken(string(class))) {
			return true
		}
	}
	return false
}
