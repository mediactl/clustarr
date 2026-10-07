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

package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// MaxFailure caps a record's lastFailure. The Indexer controller truncates
// further when it writes status.lastFailure.
const MaxFailure = 1024

// IndexerKind is the record's item kind.
const IndexerKind = "Indexer"

// Writer reports query outcomes in clustarr-indexer-health. One Writer is
// shared by the search fan-out and the RSS poll of one agent process.
type Writer struct {
	// Records writes the bucket (agentrecords.IndexerHealth).
	Records *records.Writer[*schema.IndexerHealthRecord]
	// Suppress, when set, is armed by every failure this writer reports.
	Suppress *Suppress
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

func (w *Writer) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

// Outcome reports one search query against idx: ok, or a failure of class
// (schema.IndexerFailure*, see [Classify]) for reason; indexed is the
// number of releases the query newly put in the release index.
func (w *Writer) Outcome(ctx context.Context, idx schema.ItemRef, ok bool, class, reason string, indexed int64) error {
	now := w.now()
	if !ok {
		w.Suppress.Fail(uidOf(idx), now)
	}
	return w.update(ctx, idx, func(rec *schema.IndexerHealthRecord) {
		if ok {
			rec.Successes++
			rec.LastSuccessAt = later(rec.LastSuccessAt, now)
		} else {
			fail(rec, now, class, reason)
		}
		if indexed > 0 {
			rec.IndexedReleases += indexed
		}
	})
}

// RSS reports one RSS poll of idx, started at at: newCount releases newly
// indexed when ok, or a failure of class for reason. lastRssAt is at, the
// instant the poll read the feed from, since it is the next poll's paging
// bound; the success or failure itself is stamped when it is reported.
func (w *Writer) RSS(ctx context.Context, idx schema.ItemRef, at time.Time, newCount int32, ok bool, class, reason string) error {
	now := w.now()
	if !ok {
		w.Suppress.Fail(uidOf(idx), now)
	}
	return w.update(ctx, idx, func(rec *schema.IndexerHealthRecord) {
		if !ok {
			fail(rec, now, class, reason)
			return
		}
		rec.Successes++
		rec.LastSuccessAt = later(rec.LastSuccessAt, now)
		rec.LastRssAt = later(rec.LastRssAt, at.UTC())
		rec.LastRssNewCount = newCount
		if newCount > 0 {
			rec.IndexedReleases += int64(newCount)
		}
	})
}

// update is the one read-modify-write: counters only grow, Seq goes up by
// one per write, and the header names the Indexer.
func (w *Writer) update(ctx context.Context, idx schema.ItemRef, mutate func(*schema.IndexerHealthRecord)) error {
	if w == nil || w.Records == nil {
		return errors.New("index health: no records writer is configured")
	}
	if idx.UID == "" {
		return fmt.Errorf("index health: the Indexer %s/%s has no UID", idx.Namespace, idx.Name)
	}
	idx.Kind = IndexerKind
	key := agentrecords.IndexerHealthKey(idx.UID)
	_, err := w.Records.Update(ctx, key, func(cur *schema.IndexerHealthRecord, ok bool) (*schema.IndexerHealthRecord, bool) {
		next := &schema.IndexerHealthRecord{}
		if ok {
			next = cur
		}
		next.Item = &idx
		next.Seq++
		next.State = records.StateAnswered
		mutate(next)
		return next, true
	})
	if err != nil {
		return fmt.Errorf("index health: record the outcome of %s/%s: %w", idx.Namespace, idx.Name, err)
	}
	return nil
}

func fail(rec *schema.IndexerHealthRecord, now time.Time, class, reason string) {
	rec.Failures++
	rec.LastFailureAt = later(rec.LastFailureAt, now)
	rec.LastFailure = clip(reason, MaxFailure)
	if class == "" {
		class = schema.IndexerFailureError
	}
	rec.LastFailureClass = class
}

// later is the later of cur and t, so a timestamp never moves backwards
// when two reports race in one process.
func later(cur *time.Time, t time.Time) *time.Time {
	if cur != nil && cur.After(t) {
		return cur
	}
	return &t
}

func uidOf(idx schema.ItemRef) types.UID { return types.UID(idx.UID) }

// Classify names a failed query's class for the record:
// schema.IndexerFailureTimeout for a deadline or a network timeout,
// IndexerFailureRateLimited for an indexer's own request or download limit
// or an HTTP 429, IndexerFailureError otherwise. It reads only the error's
// type, never an indexer-supplied string.
func Classify(err error) string {
	var (
		te *torznab.Error
		ne net.Error
	)
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return schema.IndexerFailureTimeout
	case errors.As(err, &te) && (te.Code == torznab.ErrRequestLimitReached ||
		te.Code == torznab.ErrDownloadLimitReached || te.HTTPStatus == http.StatusTooManyRequests):
		return schema.IndexerFailureRateLimited
	default:
		return schema.IndexerFailureError
	}
}

// clip cuts s to at most n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
