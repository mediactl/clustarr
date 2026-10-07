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

package grafttask

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

// ErrSuperseded is a task whose record has moved past it -- a newer
// dispatch, a withdrawal, an answer already in (loop spec §4.8): the pod
// does nothing.
var ErrSuperseded = errors.New("grafttask: the task is superseded")

// ErrNoRecord is a task whose record never appeared within ClaimWait.
var ErrNoRecord = errors.New("grafttask: no record to claim")

// ClaimWait is how long a pod waits for its record: the loop writes it
// before it creates the Job, and rewrites a lost one at its next pass (loop
// spec §4.7). A Job has no nak, so a pod that finds no record waits rather
// than exit at once; past ClaimWait it exits 1 and the planner's lost-Job
// row retries.
var ClaimWait = 2 * time.Minute

// AnswerTimeout is graftAnswerTimeout (loop spec §4.12): bus errors on the
// answer are retried this long.
const AnswerTimeout = 2 * time.Minute

var (
	claimPoll     = 5 * time.Second
	answerBackoff = time.Second
)

// Answerer is the worker half of clustarr-grafts Serve needs;
// *records.Answerer[*Record] implements it.
type Answerer interface {
	Superseded(ctx context.Context, key string, seq int64) (bool, error)
	Claim(ctx context.Context, key string, seq int64, reclaim bool) (records.Verdict, error)
	Answer(ctx context.Context, key string, rec *Record) (records.Verdict, error)
}

var _ Answerer = (*records.Answerer[*Record])(nil)

// Serve is one graft pod (loop spec §4.12 "Pod"): Superseded, Claim, run,
// Answer. The answer carries the task's Seq and Inputs; a bus error on it is
// retried for AnswerTimeout. It returns the run's result, ErrSuperseded for
// a task the record has moved past, ErrNoRecord for one whose record never
// appeared.
func Serve(ctx context.Context, a Answerer, t Task, run func(context.Context, Task) Result) (Result, error) {
	key := events.RecordKey(t.File.UID)
	sup, err := a.Superseded(ctx, key, t.Seq)
	if err != nil {
		return Result{}, fmt.Errorf("grafttask: read the record of %s: %w", t.File.Name, err)
	}
	if sup {
		return Result{}, ErrSuperseded
	}
	if err := waitClaim(ctx, a, key, t.Seq); err != nil {
		return Result{}, err
	}
	res := run(ctx, t)
	res.Message = Clamp(res.Message)
	answer := &Record{
		RecordHeader: schema.RecordHeader{Schema: RecordSchema, MediaFile: t.File, Seq: t.Seq, State: records.StateAnswered},
		Inputs:       t.Inputs, Answer: &res,
	}
	return res, retry(ctx, AnswerTimeout, func() error {
		_, err := a.Answer(ctx, key, answer)
		return err
	})
}

// waitClaim claims seq, waiting up to ClaimWait for a record the loop still
// owes; a record that has moved past seq is ErrSuperseded.
func waitClaim(ctx context.Context, a Answerer, key string, seq int64) error {
	deadline := time.Now().Add(ClaimWait)
	for {
		v, err := a.Claim(ctx, key, seq, false)
		if err != nil {
			return fmt.Errorf("grafttask: claim %s: %w", key, err)
		}
		switch v {
		case records.Wrote:
			return nil
		case records.Dropped, records.DroppedFact:
			return ErrSuperseded
		}
		if !time.Now().Before(deadline) {
			return ErrNoRecord
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(claimPoll):
		}
	}
}

// retry runs f until it succeeds or d has passed, waiting answerBackoff
// doubled each time up to 30 s.
func retry(ctx context.Context, d time.Duration, f func() error) error {
	deadline, wait := time.Now().Add(d), answerBackoff
	for {
		err := f()
		if err == nil || !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
		wait = min(2*wait, 30*time.Second)
	}
}
