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

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/squasharr/task"
)

// Outcome is how one Process call ended. Code classifies it the way the Job
// exit codes used to: ExitOK, ExitRetriable, ExitInvalidSource or
// ExitVerifyFailed (run.go).
type Outcome struct {
	Code       int
	Err        error
	Result     *transcodev1alpha1.Result
	StderrTail string
	Reason     task.Reason
}

// Process transcodes one task: re-probe against SourceProbeHash, plan, check
// free space, encode, verify, swap. It reads and writes files only; what the
// result means for the TranscodeJob is the caller's to report. The crash
// matrix in doc.go is unchanged because the swap order is.
func Process(ctx context.Context, t task.Task, o Options) Outcome {
	o = o.withDefaults()
	ctx, span := tracing.Start(ctx, "squasharr.worker.process") // the span Run used
	defer span.End()
	r := &runner{o: o, t: t, started: o.Now()}
	err := r.run(ctx)
	r.out.Code, r.out.Err = ExitCode(err), err
	var f *failure
	if errors.As(err, &f) && f.reason != "" {
		r.out.Reason = f.reason
	}
	r.observeOutcome(r.out.Code)
	return r.out
}
