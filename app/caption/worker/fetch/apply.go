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
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mediactl/clustarr/app/caption/datapath"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/version"
)

// searchVerdict is what a finished search means for the item.
type searchVerdict int

const (
	// verdictDownloaded: a subtitle was written.
	verdictDownloaded searchVerdict = iota
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
func decide(out searchOutcome, eligible int, onDisk bool) searchVerdict {
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
func explain(v searchVerdict, out searchOutcome, p searchPlan, outOf int32) string {
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
	return truncate(strings.Join(parts, ". "), schema.MaxSubtitleLastError)
}

// finish answers the search's verdict (loop spec §6.7.3): the record
// carries the facts, and the loop derives the item's state from them.
func (w *Worker) finish(ctx context.Context, t schema.FetchTask, p searchPlan, out searchOutcome, outOf int32) error {
	onDisk := t.OnDisk != nil
	switch v := decide(out, len(p.providers), onDisk); v {
	case verdictKeep:
		logging.FromContext(ctx).Info("fetch: nothing better than the subtitle on disk; keeping it",
			"threshold", p.threshold, "why", explain(verdictUnavailable, out, p, outOf))
		return w.answer(ctx, t, schema.SubtitleRecord{Verdict: schema.SubtitleVerdictKept, ScoreOutOf: outOf})
	case verdictDownloaded:
		ch := out.chosen
		ans := schema.SubtitleRecord{
			Verdict: schema.SubtitleVerdictDownloaded, Score: int32(ch.r.score), //nolint:gosec // bounded by MaxScore (360)
			ScoreOutOf: outOf, Provider: ch.entry.entry.Name, SubtitleID: truncate(subtitleID(ch.r.c), maxSubtitleID), Name: ch.relPath,
		}
		if onDisk && t.OnDisk.Name != ch.relPath {
			ans.ReplacedName = t.OnDisk.Name
		}
		return w.answer(ctx, t, ans)
	case verdictThrottled:
		until := out.earliest.UTC()
		logging.FromContext(ctx).Info("fetch: every eligible provider is throttled; answered, not redelivered", "earliest", until)
		return w.answer(ctx, t, schema.SubtitleRecord{
			Verdict: schema.SubtitleVerdictThrottled, ScoreOutOf: outOf,
			LastError: explain(v, out, p, outOf), ThrottledUntil: &until,
		})
	case verdictFailed:
		return w.answer(ctx, t, verdict(schema.SubtitleVerdictFailed, outOf, explain(v, out, p, outOf)))
	default:
		return w.answer(ctx, t, verdict(schema.SubtitleVerdictUnavailable, outOf, explain(v, out, p, outOf)))
	}
}

// answer records ans and, only when it lands, removes a replaced sidecar,
// counts the fetch and publishes the history event (§6.7.3-§6.7.4). An
// answer the record's rules drop changes nothing: the sidecar a dropped
// download wrote is found by the loop's next listing.
func (w *Worker) answer(ctx context.Context, t schema.FetchTask, ans schema.SubtitleRecord) error {
	landed, err := w.Store.Answer(ctx, t, ans)
	if err != nil {
		return fmt.Errorf("fetch: answer: %w", err)
	}
	if !landed {
		logging.FromContext(ctx).Warn("fetch: superseded during the fetch; the answer is dropped", "verdict", ans.Verdict)
		return nil
	}
	if ans.ReplacedName != "" {
		w.removeReplaced(ctx, t.Path, ans.ReplacedName)
	}
	metrics.SubtitleFetchesTotal.WithLabelValues(cmp.Or(ans.Provider, "none"), baseLanguage(t.LangKey), string(ans.Verdict)).Inc()
	switch ans.Verdict {
	case schema.SubtitleVerdictDownloaded:
		action, prev := events.ActionDownloaded, int32(0)
		if t.OnDisk != nil {
			action, prev = events.ActionUpgraded, t.OnDisk.Score
		}
		w.publish(ctx, t, action, func(e *schema.SubtitleEvent) {
			e.Provider, e.ProviderID, e.Score, e.PreviousScore = ans.Provider, ans.SubtitleID, ans.Score, prev
			e.Path = filepath.Join(filepath.Dir(t.Path), ans.Name)
		})
	case schema.SubtitleVerdictFailed:
		w.publish(ctx, t, events.ActionFailed, func(e *schema.SubtitleEvent) { e.Reason = ans.LastError })
	}
	return nil
}

// baseLanguage is the language half of a langKey, the metric's label.
func baseLanguage(langKey string) string {
	l, _, _, err := subtitles.ParseLangKey(subtitles.LangKey(langKey))
	if err != nil {
		return "unknown"
	}
	return l
}

// transient redelivers a failure that may clear up -- a file mid-move, a
// full disk -- and, on the final delivery, answers failed instead (§4.8's
// last delivery): the loop sees why on the file rather than only in the
// DLQ.
func (w *Worker) transient(ctx context.Context, m events.Message, t schema.FetchTask, outOf int32, cause error,
	retryAfter time.Duration,
) error {
	if !w.finalAttempt(m) {
		if retryAfter > 0 {
			return events.Retry(retryAfter, cause)
		}
		return cause
	}
	logging.FromContext(ctx).Warn("fetch: giving up on the last delivery", "err", cause)
	return w.answer(ctx, t, verdict(schema.SubtitleVerdictFailed, outOf,
		fmt.Sprintf("gave up after %d deliveries: %v", m.Attempt(), cause)))
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

// removeReplaced deletes the sidecar an upgrade superseded when it had a
// different name (an .ass replaced by an .srt, say). Same-name upgrades were
// already replaced atomically by the write. Only a bare file name is ever
// removed -- never a path that could leave the media file's directory.
func (w *Worker) removeReplaced(ctx context.Context, mediaPath, oldName string) {
	if oldName == "" || filepath.Base(oldName) != oldName || oldName == "." || oldName == ".." {
		return
	}
	local, err := datapath.Local(w.DataDir, filepath.Join(filepath.Dir(mediaPath), oldName))
	if err != nil {
		return
	}
	if err := os.Remove(local); err != nil && !errors.Is(err, os.ErrNotExist) {
		logging.FromContext(ctx).Warn("fetch: could not remove the replaced sidecar", "path", local, "err", err)
	}
}

// publish emits clustarr.evt.subtitle.subtitle.<action>.<mediafile-uid>. It
// is best effort and runs only after the answer landed: the event is
// history, the record is the record, and failing the task over a lost event
// would redeliver a fetch that already wrote its sidecar and spent a
// download from the provider's quota.
func (w *Worker) publish(ctx context.Context, t schema.FetchTask, action string, fill func(*schema.SubtitleEvent)) {
	now := w.now()
	evt := schema.SubtitleEvent{MediaFile: t.MediaFile, Action: action, LangKey: t.LangKey, Seq: t.Seq, At: now}
	fill(&evt)
	schemaName, data, err := schema.Encode(evt)
	if err != nil {
		logging.FromContext(ctx).Warn("fetch: could not encode the subtitle event", "err", err)
		return
	}
	env := &events.Envelope{
		ID:     fmt.Sprintf("%s/subtitle/%s/%d/%s", t.MediaFile.UID, t.LangKey, t.Seq, action),
		Type:   schema.SubtitleEventType,
		Schema: schemaName,
		Source: "agent-caption@" + version.String(),
		Key:    t.MediaFile.Key(),
		Time:   now,
		Data:   data,
	}
	tracing.Inject(ctx, env)
	if _, err := w.Bus.Publish(ctx, events.SubtitleEventSubject(action, t.MediaFile.UID), env); err != nil {
		logging.FromContext(ctx).Warn("fetch: could not publish the subtitle event", "action", action, "err", err)
	}
}
