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

package search

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/wantedcron"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
)

// handleWantedScan expands one catalog.WantedScan.v1 into one
// catalog.SearchTask.v1 per eligible item in the namespace.
//
// The split is spec §5's: wantedcron publishes ONE message per namespace onto
// clustarr.work.catalogarr.wantedscan.low.<namespace>, which the
// catalogarr-search-normal consumer already filters, and the search worker
// fans it out. catalogarr/controller/wantedcron/doc.go states the same
// contract from the other side; §6.1's per-item backoff therefore runs here,
// against wantedcron's own exported Backoff/NextEligible/Eligible so the two
// halves cannot drift.
//
// Fanning out by publishing, rather than searching inline, is what keeps the
// sweep inside its 120s AckWait: a namespace with hundreds of wanted items
// would need hours of RPCs in one delivery, and a single failure near the end
// would redo all of them. Each published task instead gets its own ack,
// its own backoff and its own dead-letter.
func (w *Worker) handleWantedScan(ctx context.Context, span trace.Span, m events.Message) error {
	env := m.Envelope()

	var scan schema.WantedScan
	if err := schema.Decode(env.Schema, env.Data, &scan); err != nil {
		return events.Discard("undecodable WantedScan", err)
	}

	ns := scan.Namespace
	if ns == "" {
		// The payload is the carrier of record here (the subject's last token
		// is the namespace too, but tok() has already flattened it); the
		// envelope key is the fallback.
		if prefix, _, ok := strings.Cut(env.Key, "/"); ok && prefix != "" {
			ns = prefix
		}
	}
	if ns == "" {
		return events.Discard("WantedScan names no namespace",
			fmt.Errorf("key=%q", env.Key))
	}
	ctx = logging.With(ctx, "namespace", ns, "epoch", scan.Epoch)

	if w.Publisher == nil {
		// Not a Discard: this is a wiring bug, and acking would swallow the
		// whole sweep. Let it nak and be visible.
		return errors.New("search: no Publisher wired; cannot expand a WantedScan")
	}

	items, err := w.wantedItems(ctx, ns, scan)
	if err != nil {
		tracing.RecordError(span, err)
		return err
	}

	published := 0
	for _, item := range items {
		if err := w.publishItemSearch(ctx, ns, item, scan.Epoch); err != nil {
			if errors.Is(err, events.ErrQueueFull) {
				// Back-pressure. Nak the whole sweep: the items already
				// published carry a Msg-Id derived from this sweep's epoch,
				// so the redelivery deduplicates them inside the stream's
				// one-hour window instead of searching them twice.
				w.log(ctx).Info("search: wanted sweep hit a full queue; retrying",
					"published", published, "remaining", len(items)-published)
				return events.Retry(wantedScanRetry, err)
			}
			tracing.RecordError(span, err)
			return err
		}
		published++
	}

	w.log(ctx).Info("search: wanted sweep expanded", "eligible", len(items), "published", published)
	return nil
}

// wantedScanRetry is how long to wait before retrying a sweep the work queue
// refused. It is generous because a full catalogarr work stream means the
// search workers are already saturated; the sweep is the lowest-value traffic
// on it and should yield.
const wantedScanRetry = 5 * time.Minute

// wantedItem is one catalog item the sweep decided is worth searching for.
type wantedItem struct {
	Ref    commonv1.MediaRef
	Reason schema.SearchReason
	UID    string
}

// wantedItems lists the namespace's movies and episodes and keeps the ones
// that are still missing something AND whose per-item backoff has elapsed.
//
// The phase predicates restate wantedcron's own (movieWanted/episodeWanted
// there are unexported); the backoff is wantedcron's exported Eligible, so the
// half that decides "wake this namespace" and the half that decides "search
// this item" cannot disagree about the ladder. If the phase predicates are
// ever exported, delete these two.
func (w *Worker) wantedItems(ctx context.Context, ns string, scan schema.WantedScan) ([]wantedItem, error) {
	now := w.now()
	want := kindFilter(scan.Kinds)
	var out []wantedItem

	if want(commonv1.MediaKindMovie) {
		var movies catalogv1alpha1.MovieList
		if err := w.Client.List(ctx, &movies, client.InNamespace(ns)); err != nil {
			return nil, fmt.Errorf("list Movies in %s: %w", ns, err)
		}
		for i := range movies.Items {
			m := &movies.Items[i]
			reason, ok := movieSearchReason(m.Status.Phase, scan.CutoffUnmet)
			if !ok {
				continue
			}
			if !wantedcron.Eligible(searchAttempts(m.Status.SearchAttempts, m.Status.LastSearchedAt), now) {
				continue
			}
			out = append(out, wantedItem{
				Ref:    commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: m.Name},
				Reason: reason,
				UID:    string(m.UID),
			})
		}
	}

	if want(commonv1.MediaKindEpisode) {
		var episodes catalogv1alpha1.EpisodeList
		if err := w.Client.List(ctx, &episodes, client.InNamespace(ns)); err != nil {
			return nil, fmt.Errorf("list Episodes in %s: %w", ns, err)
		}
		for i := range episodes.Items {
			ep := &episodes.Items[i]
			reason, ok := episodeSearchReason(ep.Status.Phase, scan.CutoffUnmet)
			if !ok {
				continue
			}
			if !wantedcron.Eligible(searchAttempts(ep.Status.SearchAttempts, ep.Status.LastSearchedAt), now) {
				continue
			}
			out = append(out, wantedItem{
				Ref:    commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: ep.Name},
				Reason: reason,
				UID:    string(ep.UID),
			})
		}
	}
	return out, nil
}

// kindFilter turns WantedScan.Kinds into a predicate. An empty list means
// every kind, per the payload's own doc comment.
func kindFilter(kinds []commonv1.MediaKind) func(commonv1.MediaKind) bool {
	if len(kinds) == 0 {
		return func(commonv1.MediaKind) bool { return true }
	}
	set := make(map[commonv1.MediaKind]struct{}, len(kinds))
	for _, k := range kinds {
		set[k] = struct{}{}
	}
	return func(k commonv1.MediaKind) bool {
		_, ok := set[k]
		return ok
	}
}

// movieSearchReason maps a Movie's phase onto the SearchReason a sweep would
// publish, or reports that the phase is not worth searching for. Wanted means
// there is no file at all; CutoffUnmet means there is one but it is below the
// profile cutoff, and the sweep only chases those when it was asked to.
func movieSearchReason(p catalogv1alpha1.MoviePhase, cutoffUnmet bool) (schema.SearchReason, bool) {
	switch p {
	case catalogv1alpha1.MoviePhaseWanted:
		return schema.SearchReasonMissing, true
	case catalogv1alpha1.MoviePhaseCutoffUnmet:
		return schema.SearchReasonCutoffUnmet, cutoffUnmet
	default:
		return "", false
	}
}

func episodeSearchReason(p catalogv1alpha1.EpisodePhase, cutoffUnmet bool) (schema.SearchReason, bool) {
	switch p {
	case catalogv1alpha1.EpisodePhaseWanted:
		return schema.SearchReasonMissing, true
	case catalogv1alpha1.EpisodePhaseCutoffUnmet:
		return schema.SearchReasonCutoffUnmet, cutoffUnmet
	default:
		return "", false
	}
}

// searchAttempts folds status.lastSearchedAt into status.searchAttempts.
//
// It mirrors wantedcron's unexported helper of the same name, for the same
// reason: the two fields overlap, Attempts.Latest is the structured record and
// LastSearchedAt the flat one, and a writer may reasonably set only the
// latter. Taking the later of the two stops an item searched five minutes ago
// from being searched again because the write went to the other field.
func searchAttempts(a commonv1.Attempts, lastSearchedAt *metav1.Time) commonv1.Attempts {
	if lastSearchedAt == nil {
		return a
	}
	if a.Latest == nil || lastSearchedAt.After(a.Latest.Time) {
		a.Latest = lastSearchedAt
	}
	return a
}

// publishItemSearch enqueues one item's search at the normal tier.
//
// The message id is (item UID, sweep epoch, "search"): a redelivery of the
// same sweep deduplicates inside the stream's one-hour window, so a sweep that
// naks halfway through does not re-search what it already published, while the
// next sweep twelve hours later carries a different epoch and is free to run.
// That is exactly what WantedScan.Epoch is for.
func (w *Worker) publishItemSearch(ctx context.Context, ns string, item wantedItem, epoch int64) error {
	schemaName, data, err := schema.Encode(schema.SearchTask{
		MediaRef: item.Ref,
		Reason:   item.Reason,
	})
	if err != nil {
		return err
	}
	mediaKey := events.MediaKey(string(item.Ref.Kind), ns, item.Ref.Name)
	env := &events.Envelope{
		ID:     events.MsgIDForObject(item.UID, epoch, "search"),
		Type:   "catalog.SearchTask",
		Schema: schemaName,
		Source: "catalogarr@" + version.String(),
		// The envelope key names the owning object, per events.Envelope; the
		// mediaKey identifies the item and belongs in the subject, where the
		// grab lease derives from the same token.
		Key:  ns + "/" + item.Ref.Name,
		Time: w.now(),
		Data: data,
	}
	_, err = w.Publisher.Publish(ctx, events.WorkSearchSubject(events.PriorityNormal, mediaKey), env)
	return err
}
