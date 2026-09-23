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

package fetch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/captionarr/status"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
)

// verdict is what a finished search means for the item.
type verdict int

const (
	// verdictDownloaded: a subtitle was written.
	verdictDownloaded verdict = iota
	// verdictKeep: a subtitle is already on disk and nothing better was
	// found (or nothing could be asked). The item is left exactly as it is.
	verdictKeep
	// verdictUnavailable: providers answered, and nothing reached the
	// threshold -- or no provider can serve this item at all.
	verdictUnavailable
	// verdictFailed: acceptable candidates existed but none could be
	// fetched, or every provider asked failed.
	verdictFailed
	// verdictThrottled: every provider that could serve the item is in a
	// throttle window, so nothing was asked.
	verdictThrottled
)

// decide maps a search outcome to a verdict. Pure, so every branch of the
// ack-or-record policy is table-testable without a cluster.
func decide(out searchOutcome, eligible int, onDisk bool) verdict {
	switch {
	case out.chosen != nil:
		return verdictDownloaded
	case onDisk:
		return verdictKeep
	case eligible == 0:
		return verdictUnavailable
	case out.acceptable > 0:
		return verdictFailed
	case out.searched > 0:
		return verdictUnavailable
	case len(out.providerErrors) > 0:
		return verdictFailed
	case len(out.throttled) > 0:
		return verdictThrottled
	default:
		return verdictUnavailable
	}
}

// explain renders why nothing was written, for items[].lastError.
func explain(v verdict, out searchOutcome, p searchPlan, outOf int32) string {
	var parts []string
	switch v {
	case verdictUnavailable:
		switch {
		case len(p.providers) == 0:
			parts = append(parts, fmt.Sprintf("no enabled SubtitleProvider can search %s subtitles in %s", p.kind, p.want.lang))
			if len(p.skipped) > 0 {
				parts = append(parts, "skipped: "+strings.Join(p.skipped, "; "))
			}
		case out.bestBelow > 0:
			parts = append(parts, fmt.Sprintf("no candidate reached the minimum score %d/%d (best %d from %s)",
				p.threshold, outOf, out.bestBelow, out.bestBelowFrom))
		default:
			parts = append(parts, "no matching subtitle found")
		}
	case verdictFailed:
		if out.acceptable > 0 {
			parts = append(parts, fmt.Sprintf("%d acceptable candidate(s), none could be fetched", out.acceptable))
		} else {
			parts = append(parts, "every provider asked failed")
		}
	case verdictThrottled:
		parts = append(parts, "every eligible provider is throttled")
	}
	if len(out.fetchErrors) > 0 {
		parts = append(parts, "fetch errors: "+strings.Join(out.fetchErrors, "; "))
	}
	if len(out.providerErrors) > 0 {
		parts = append(parts, "provider errors: "+strings.Join(out.providerErrors, "; "))
	}
	if len(out.throttled) > 0 {
		parts = append(parts, "throttled: "+strings.Join(out.throttled, "; "))
	}
	return truncate(strings.Join(parts, ". "), maxLastError)
}

// finish records the search's verdict on the item and publishes the event.
func (w *Worker) finish(ctx context.Context, req *subtitlev1alpha1.SubtitleRequest, langKey string, p searchPlan,
	out searchOutcome, onDisk bool, cur *subtitlev1alpha1.SubtitleItem, outOf int32, upgradesEnabled bool,
) error {
	log := logging.FromContext(ctx)
	v := decide(out, len(p.providers), onDisk)
	now := metav1.NewTime(w.now())

	switch v {
	case verdictKeep:
		log.Info("fetch: nothing better than the subtitle on disk; keeping it",
			"score", cur.Score, "threshold", p.threshold, "why", explain(verdictUnavailable, out, p, outOf))
		return nil

	case verdictDownloaded:
		ch := out.chosen
		score := int32(ch.r.score) //nolint:gosec // bounded by MaxScore (360)
		state := subtitlev1alpha1.SubtitleItemDownloaded
		if upgradesEnabled && score < outOf-upgradeMargin {
			state = subtitlev1alpha1.SubtitleItemUpgradable
		}
		applied, err := w.record(ctx, req, langKey, func(it *subtitlev1alpha1.SubtitleItem) {
			it.State = state
			it.Score, it.ScoreOutOf = score, outOf
			it.Provider = ch.entry.entry.Name
			it.SubtitleID = subtitleID(ch.r.c)
			it.Path = ch.relPath
			it.DownloadedAt = &now
			it.LastError = ""
		})
		if err != nil {
			return err
		}
		if !applied {
			log.Warn("fetch: the subtitle request went away during the fetch; the sidecar is written but unrecorded",
				"path", ch.logicalPath)
			return nil
		}
		action, prev := events.ActionDownloaded, int32(0)
		if onDisk {
			action, prev = events.ActionUpgraded, cur.Score
			w.removeReplaced(ctx, p.mediaPath, cur.Path, ch.relPath)
		}
		w.publish(ctx, req, action, langKey, ch.entry.entry.Name+"/"+subtitleID(ch.r.c), func(e *schema.SubtitleEvent) {
			e.Provider = ch.entry.entry.Name
			e.ProviderID = subtitleID(ch.r.c)
			e.Score, e.PreviousScore = score, prev
			e.Path = ch.logicalPath
		})
		return nil

	case verdictThrottled:
		msg := explain(v, out, p, outOf)
		log.Info("fetch: every eligible provider is throttled; recorded, not redelivered", "earliest", out.earliest)
		_, err := w.record(ctx, req, langKey, func(it *subtitlev1alpha1.SubtitleItem) {
			if it.State == "" {
				it.State = subtitlev1alpha1.SubtitleItemPending
			}
			it.LastError = msg
		})
		return err

	default: // verdictUnavailable, verdictFailed
		state := subtitlev1alpha1.SubtitleItemUnavailable
		if v == verdictFailed {
			state = subtitlev1alpha1.SubtitleItemFailed
		}
		msg := explain(v, out, p, outOf)
		log.Info("fetch: no subtitle written", "state", state, "why", msg)
		applied, err := w.record(ctx, req, langKey, func(it *subtitlev1alpha1.SubtitleItem) {
			clearCandidate(it, state, outOf, msg)
		})
		if err != nil {
			return err
		}
		if applied && v == verdictFailed {
			w.publishFailure(ctx, req, langKey, msg)
		}
		return nil
	}
}

// clearCandidate records a no-subtitle outcome: the state and why, and no
// chosen candidate -- the item has nothing on disk.
func clearCandidate(it *subtitlev1alpha1.SubtitleItem, state subtitlev1alpha1.SubtitleItemState, outOf int32, msg string) {
	it.State = state
	it.Score, it.ScoreOutOf = 0, outOf
	it.Provider, it.SubtitleID, it.Path = "", "", ""
	it.DownloadedAt = nil
	it.LastError = msg
}

// recordFailure records a failure that no redelivery will fix and acks.
func (w *Worker) recordFailure(ctx context.Context, req *subtitlev1alpha1.SubtitleRequest, langKey string, outOf int32, msg string) error {
	logging.FromContext(ctx).Warn("fetch: recording a failure", "why", msg)
	applied, err := w.record(ctx, req, langKey, func(it *subtitlev1alpha1.SubtitleItem) {
		if hasSubtitle(it) {
			it.LastError = msg // the subtitle on disk is still good
			return
		}
		clearCandidate(it, subtitlev1alpha1.SubtitleItemFailed, outOf, msg)
	})
	if err != nil {
		return err
	}
	if applied {
		w.publishFailure(ctx, req, langKey, msg)
	}
	return nil
}

// transient redelivers a failure that may clear up -- a file mid-move, a
// full disk -- and, on the final delivery, records it on the item and acks
// instead, the same shape as importarr/worker/fileimport's finishBlocked:
// the operator sees why on the object rather than only in the DLQ.
func (w *Worker) transient(ctx context.Context, m events.Message, req *subtitlev1alpha1.SubtitleRequest, langKey string,
	outOf int32, cause error, retryAfter time.Duration,
) error {
	if !w.finalAttempt(m) {
		if retryAfter > 0 {
			return events.Retry(retryAfter, cause)
		}
		return cause
	}
	msg := truncate(fmt.Sprintf("gave up after %d deliveries: %v", m.Attempt(), cause), maxLastError)
	return w.recordFailure(ctx, req, langKey, outOf, msg)
}

// finalAttempt reports whether m is the last delivery the fetch consumers
// allow.
func (w *Worker) finalAttempt(m events.Message) bool {
	md := w.MaxDeliver
	if md <= 0 {
		if spec, ok := events.Default().Consumer(events.ConsumerCaptionFetchNormal); ok {
			md = spec.MaxDeliver
		}
	}
	return md <= 0 || m.Attempt() >= uint64(md) //nolint:gosec // MaxDeliver is a small positive constant
}

// record applies one item's worker-owned leaves.
//
// It RE-READS the SubtitleRequest first, through the uncached API reader,
// and seeds the apply from that read -- never from req, which was read
// before the provider walk. This is CLAUDE.md's lost-update shape exactly: a
// fetch spends seconds to minutes on provider round trips, and
// status.RequestWorkerFields re-declares every worker-owned leaf of EVERY
// item, so an apply seeded from the pre-search snapshot would silently roll
// back whatever another fetch worker recorded for a sibling language in the
// meantime -- a sidecar written, recorded, then forgotten, and dropped from
// MediaFile.status.sidecars by catalogarr. No release test can see that;
// the interleaved-writer test in worker_envtest_test.go does. (The
// controller's own leaves, nextSearchAt and attempts, are a different
// manager's and are never declared here, so they survive this apply either
// way.)
//
// set mutates the item for langKey, which is created when the request has
// none yet; set must leave a non-empty state, which the CRD requires. The
// bool is false when there was nothing to apply to: the request is gone, was
// replaced, or has no room for another item.
func (w *Worker) record(ctx context.Context, req *subtitlev1alpha1.SubtitleRequest, langKey string,
	set func(*subtitlev1alpha1.SubtitleItem),
) (bool, error) {
	var fresh subtitlev1alpha1.SubtitleRequest
	if err := w.reader().Get(ctx, types.NamespacedName{Namespace: req.Namespace, Name: req.Name}, &fresh); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("fetch: re-read subtitle request before the status apply: %w", err)
	}
	if fresh.UID != req.UID {
		return false, nil
	}

	items := make([]subtitlev1alpha1.SubtitleItem, len(fresh.Status.Items))
	for i := range fresh.Status.Items {
		fresh.Status.Items[i].DeepCopyInto(&items[i])
	}
	idx := -1
	for i := range items {
		if items[i].LangKey == langKey {
			idx = i
			break
		}
	}
	if idx < 0 {
		if len(items) >= maxItems {
			logging.FromContext(ctx).Warn("fetch: the request already carries the maximum number of items; not adding one",
				"max", maxItems)
			return false, nil
		}
		items = append(items, subtitlev1alpha1.SubtitleItem{LangKey: langKey})
		idx = len(items) - 1
	}
	set(&items[idx])
	fresh.Status.Items = items

	if err := status.PatchRequest(ctx, w.Client, FieldManager, &fresh, nil); err != nil {
		return false, fmt.Errorf("fetch: apply subtitle request status: %w", err)
	}
	return true, nil
}

// removeReplaced deletes the sidecar an upgrade superseded when it had a
// different name (an .ass replaced by an .srt, say). Same-name upgrades were
// already replaced atomically by the write. Only a bare file name the worker
// itself recorded is ever removed -- never a path that could leave the
// media file's directory.
func (w *Worker) removeReplaced(ctx context.Context, mediaPath, oldRel, newRel string) {
	if oldRel == "" || oldRel == newRel || filepath.Base(oldRel) != oldRel || oldRel == "." || oldRel == ".." {
		return
	}
	local, err := localPath(w.dataDir(), filepath.Join(filepath.Dir(mediaPath), oldRel))
	if err != nil {
		return
	}
	if err := os.Remove(local); err != nil && !errors.Is(err, os.ErrNotExist) {
		logging.FromContext(ctx).Warn("fetch: could not remove the replaced sidecar", "path", local, "err", err)
	}
}

// publishFailure publishes a subtitle.failed event.
func (w *Worker) publishFailure(ctx context.Context, req *subtitlev1alpha1.SubtitleRequest, langKey, reason string) {
	w.publish(ctx, req, events.ActionFailed, langKey, strconv.FormatInt(w.now().UnixNano(), 10), func(e *schema.SubtitleEvent) {
		e.Reason = reason
	})
}

// publish emits clustarr.evt.subtitle.subtitle.<action>.<request-uid>. It is
// best effort and runs only after the status apply landed: the event is
// history, the status is the record, and failing the task over a lost event
// would redeliver a fetch that already wrote its sidecar and spent a
// download from the provider's quota.
func (w *Worker) publish(ctx context.Context, req *subtitlev1alpha1.SubtitleRequest, action, langKey, idSuffix string,
	fill func(*schema.SubtitleEvent),
) {
	now := w.now()
	evt := schema.SubtitleEvent{
		RequestRef: schema.Ref{Namespace: req.Namespace, Name: req.Name, UID: string(req.UID)},
		Action:     action,
		LangKey:    langKey,
		At:         now,
	}
	fill(&evt)
	schemaName, data, err := schema.Encode(evt)
	if err != nil {
		logging.FromContext(ctx).Warn("fetch: could not encode the subtitle event", "err", err)
		return
	}
	env := &events.Envelope{
		ID:     strings.Join([]string{string(req.UID), langKey, action, idSuffix}, "/"),
		Type:   "subtitle.SubtitleEvent",
		Schema: schemaName,
		Source: "captionarr-worker@" + version.String(),
		Key:    req.Namespace + "/" + req.Name,
		Time:   now,
		Data:   data,
	}
	tracing.Inject(ctx, env)
	if _, err := w.Bus.Publish(ctx, events.SubtitleEventSubject(action, string(req.UID)), env); err != nil {
		logging.FromContext(ctx).Warn("fetch: could not publish the subtitle event", "action", action, "err", err)
	}
}
