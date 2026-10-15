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

package worker

import (
	"context"
	"errors"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/segments"
)

// tally is what a task did with its files.
type tally struct {
	files                             int
	found, notFound, errored, amended int
	// deferred files were left unrecorded by a transient failure: retried
	// with the season, not recorded as Errors.
	deferred  int
	abandoned bool
}

// count notes a recorded result.
func (t *tally) count(res analysisResult, amend bool) {
	switch {
	case amend:
		t.amended++
	case res.Result == segments.ResultFound:
		t.found++
	case res.Result == segments.ResultNotFound:
		t.notFound++
	case res.Result == segments.ResultError:
		t.errored++
	}
}

// logFinished is the task's one INFO line: what became of it and of its
// files, so a pod's log says what it did and not only what FFmpeg warned.
func (h *Handler) logFinished(ctx context.Context, m events.Message, key string, task schema.AnalyzeTask, t tally, took time.Duration, err error) {
	if key == "" { // the envelope's key is the task's: "<namespace>/<series>-sNN" or the movie's
		key = task.Key
	}
	attrs := []any{
		"key", key, "kind", task.Kind, "attempt", m.Attempt(),
		"files", t.files, "found", t.found, "notFound", t.notFound, "error", t.errored,
		"amended", t.amended, "deferred", t.deferred,
		"duration", took.Round(time.Millisecond), "abandoned", t.abandoned,
		"outcome", outcome(err, m.Attempt(), h.maxDeliver()),
	}
	if err != nil {
		attrs = append(attrs, "reason", err.Error())
	}
	logging.FromContext(ctx).InfoContext(ctx, "markers: task finished", attrs...)
}

// outcome is how the bus settles err on delivery attempt of a durable that
// delivers maxDeliver times (events.Settle).
func outcome(err error, attempt uint64, maxDeliver int) string {
	var d *events.DiscardError
	var rq *events.RequeueError
	switch {
	case err == nil:
		return "acked"
	case errors.As(err, &d):
		return "dead-lettered"
	case errors.As(err, &rq):
		return "requeued"
	case maxDeliver > 0 && attempt >= uint64(maxDeliver):
		return "dead-lettered"
	default:
		return "retrying"
	}
}
