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

	"github.com/mediactl/clustarr/app/transcode/grafttask"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Outcome is how one Process call ended. Code classifies it the way the Job
// exit codes used to: ExitOK, ExitRetriable, ExitInvalidSource or
// ExitVerifyFailed (run.go).
type Outcome struct {
	Code       int
	Err        error
	Result     *task.Result
	StderrTail string
	Reason     task.Reason
	// Graft is what came of the task's graft, when it carried one.
	Graft *grafttask.Result
}

// Process transcodes one task: re-probe against SourceProbeHash, plan, check
// free space, encode, verify, swap. It reads and writes files only; what the
// result means for the MediaFile is the caller's to answer. The crash
// matrix in doc.go is unchanged because the swap order is.
func Process(ctx context.Context, t task.Task, o Options) Outcome {
	o = o.withDefaults()
	ctx, span := tracing.Start(ctx, "transcode.worker.process") // the span Run used
	defer span.End()
	// Every log line run() emits below -- and any a caller emits with this
	// ctx afterward -- carries the task's MediaFile and dispatch.
	ctx = logging.With(ctx, "mediaFile", t.File.Namespace+"/"+t.File.Name, "seq", t.Seq)
	r := &runner{o: o, t: t, started: o.Now()}
	err := r.run(ctx)
	r.out.Code, r.out.Err = ExitCode(err), err
	var f *failure
	if errors.As(err, &f) && f.reason != "" {
		r.out.Reason = f.reason
	}
	if err != nil {
		tracing.RecordError(span, err)
	}
	r.observeOutcome(r.out.Code)
	return r.out
}
