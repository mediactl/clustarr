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

package markers

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	catalogmarkers "github.com/mediactl/clustarr/app/catalog/markers"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/records"
)

// ErrNotAsked is a NotFound decided without asking a provider.
var ErrNotAsked = errors.New("not asked")

// errNoProvider is a gateway whose registry has no markers provider: a
// disabled theintrodb, or the seed landing after the gateway built its
// registry. The task is acked without an answer, so the record stays
// requested and the loop asks again after its request timeout; a retry would
// cycle every due file to the dead-letter stream, and an Error would park it
// for ErrorTTL.
var errNoProvider = errors.New("no markers provider is configured")

// Handler is the catalogarr-markers durable's handler: it asks TheIntroDB
// for one file and writes the answer into clustarr-markers by CAS (loop spec
// 2026-10-06 §4.12). It reads no Kubernetes object: the task carries the
// query, which the remediation loop built from its cache.
type Handler struct {
	Providers  []metadata.MarkersProvider
	Clock      func() time.Time
	Answers    *catalogmarkers.Answers // writes as this pod (NewAnswers' writer)
	Bus        events.Publisher        // a deferral's republish at the reset
	MaxDeliver uint64                  // the consumer's; the last delivery answers failed

	// absent remembers, for SeriesAbsentTTL, each series the provider has
	// nothing for at all (metadata.ErrNoTitle), keyed by the task's
	// SeriesKey, so its other episodes are not asked about one by one.
	mu     sync.Mutex
	absent map[string]time.Time
}

// deferAfter is the longest limit a task waits out in its in-flight slot.
// A longer one -- TheIntroDB's daily allowance -- defers the record and is
// published again for the reset, so the durable's slots serve the tasks that
// need no request.
const deferAfter = 5 * time.Minute

// SeriesAbsentTTL is how long a series the provider lacks answers for its
// other episodes without a request. The domain runs one replica, and a
// restart costs one request per series.
const SeriesAbsentTTL = 24 * time.Hour

// Handle implements events.Handler.
func (h *Handler) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	if env.Schema == "catalog.MarkersTask.v1" {
		return nil // published before the switch: the file's next due pass asks with a v2 task
	}
	var task schema.MarkersTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("undecodable markers task", err)
	}
	if task.File.UID == "" || task.Seq == 0 {
		return events.Discard("a markers task with no file or sequence", fmt.Errorf("file=%v seq=%d", task.File, task.Seq))
	}
	if sup, err := h.Answers.Superseded(ctx, task); err != nil {
		return err
	} else if sup {
		return nil
	}
	now := h.now()
	q := metadata.MarkersQuery{
		IDs: metadata.ExternalIDs(task.Inputs.Query.IDs), Season: task.Inputs.Query.Season,
		Episode: task.Inputs.Query.Episode, DurationMs: task.Inputs.DurationMs,
	}
	var segs metadata.Segments
	var err error
	if h.seriesAbsent(task.SeriesKey, now) {
		err = fmt.Errorf("the series is not on TheIntroDB: %w: %w", ErrNotAsked, metadata.ErrNotFound)
	} else if segs, err = h.ask(ctx, q); errors.Is(err, metadata.ErrNoTitle) {
		h.rememberAbsent(task.SeriesKey, now)
	}
	var rl *metadata.RateLimitedError
	switch {
	case errors.As(err, &rl) && rl.RetryAfter > deferAfter && h.Bus != nil:
		until := now.Add(rl.RetryAfter)
		v, derr := h.Answers.Defer(ctx, task, until)
		if derr != nil {
			return events.Retry(rl.RetryAfter, errors.Join(err, derr))
		}
		if v != records.Wrote {
			return nil // superseded or answered meanwhile: nothing to defer
		}
		subject, id, denv, perr := catalogmarkers.DeferredTask(task, now, until)
		if perr == nil {
			_, perr = h.Bus.Publish(ctx, subject, denv, events.WithMsgID(id), events.WithScheduleAt(until))
		}
		if perr != nil {
			return events.Retry(rl.RetryAfter, errors.Join(err, perr))
		}
		return nil
	case errors.As(err, &rl):
		if h.MaxDeliver > 0 && m.Attempt() >= h.MaxDeliver {
			// The last delivery answers failed and transient (§4.8), never
			// leaving the request to the dead-letter stream.
			_, aerr := h.Answers.Answer(ctx, task, schema.MarkersAnswer{FetchedAt: now, Message: err.Error()}, true)
			return aerr
		}
		return events.Retry(rl.RetryAfter, err)
	case errors.Is(err, errNoProvider):
		return nil // the registry, not the file: the record stays requested and the loop asks again after 24 h
	}
	ans := schema.MarkersAnswer{FetchedAt: now}
	switch {
	case err == nil:
		ans.Result, ans.Segments = string(catalogv1alpha1.MarkersFound), toJSON(segs)
	case errors.Is(err, metadata.ErrNotFound):
		ans.Result = string(catalogv1alpha1.MarkersNotFound)
		if errors.Is(err, ErrNotAsked) {
			ans.Message = err.Error()
		}
	default:
		logging.FromContext(ctx).WarnContext(ctx, "markers: fetch failed", "mediafile", task.File.Name, "error", err)
		ans.Result, ans.Message = string(catalogv1alpha1.MarkersError), err.Error()
	}
	_, err = h.Answers.Answer(ctx, task, ans, false)
	return err
}

// seriesAbsent reports whether the provider had nothing for series k (the
// task's SeriesKey; "" for a movie) inside SeriesAbsentTTL.
func (h *Handler) seriesAbsent(k string, now time.Time) bool {
	if k == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	at, ok := h.absent[k]
	return ok && now.Sub(at) < SeriesAbsentTTL
}

func (h *Handler) rememberAbsent(k string, now time.Time) {
	if k == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.absent == nil {
		h.absent = map[string]time.Time{}
	}
	h.absent[k] = now
}

// ask takes the first provider's answer; NotFound from every one is
// NotFound, and any other failure is the error.
func (h *Handler) ask(ctx context.Context, q metadata.MarkersQuery) (metadata.Segments, error) {
	if len(h.Providers) == 0 {
		return metadata.Segments{}, errNoProvider
	}
	var last error
	for _, p := range h.Providers {
		segs, err := p.Markers(ctx, q)
		if err == nil {
			return segs, nil
		}
		last = err
		if !errors.Is(err, metadata.ErrNotFound) {
			return metadata.Segments{}, err
		}
	}
	return metadata.Segments{}, last
}

func (h *Handler) now() time.Time {
	if h.Clock != nil {
		return h.Clock()
	}
	return time.Now()
}

// toJSON tags TheIntroDB's segments with their source, confidence 100, as
// the answer carries them; the loop's merge orders and caps them.
func toJSON(s metadata.Segments) []schema.SegmentJSON {
	var out []schema.SegmentJSON
	for _, k := range []struct {
		kind catalogv1alpha1.MarkerKind
		list []metadata.Segment
	}{
		{catalogv1alpha1.MarkerIntro, s.Intro},
		{catalogv1alpha1.MarkerRecap, s.Recap},
		{catalogv1alpha1.MarkerCredits, s.Credits},
		{catalogv1alpha1.MarkerPreview, s.Preview},
	} {
		for _, x := range k.list {
			out = append(out, schema.SegmentJSON{
				Kind: string(k.kind), StartMs: x.StartMs, EndMs: x.EndMs,
				Source: string(catalogv1alpha1.SegmentSourceTheIntroDB), Confidence: 100,
			})
		}
	}
	return out
}
