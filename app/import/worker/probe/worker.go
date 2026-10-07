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

// Package probe is the import domain's probe worker (spec 2026-10-06 §6.6):
// it answers catalogarr's MediaFile probe tasks (importarr-probe-high and
// importarr-probe-low) into the clustarr-probes record the MediaFile
// reconciler reads, through the process's one mediainfo.Prober. It writes
// only to NATS: no Kubernetes client, and no prober of its own.
package probe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/probestore"
)

// TaskTimeout bounds one probe. It fits inside BackOff[0] (60 s) of both
// probe consumers with room for the record reads and the stats, so the
// consumers need no heartbeat (TestTheProbeFitsTheProbeConsumersAckDeadline).
const TaskTimeout = 45 * time.Second

// defaultDataRoot is where the import domain mounts the library.
const defaultDataRoot = "/data"

// Probe outcomes, metrics.ProbeDuration's outcome label.
const (
	outcomeProbed     = "probed"
	outcomeFailed     = "failed"
	outcomeTransient  = "transient"
	outcomeAbandoned  = "abandoned"
	outcomeSuperseded = "superseded"
)

// Options name the pod (recorded as ProbeRecord.Prober) and the data mount; a
// task whose path is not under DataRoot is refused. An empty DataRoot is /data.
type Options struct {
	Pod      string
	DataRoot string
}

// Worker answers probe tasks.
type Worker struct {
	Store  *probestore.Store
	Prober mediainfo.Prober
	Opts   Options
}

// NewWorker is a worker answering through prober into bus's clustarr-probes.
func NewWorker(bus probestore.Bus, prober mediainfo.Prober, o Options) *Worker {
	if o.DataRoot == "" {
		o.DataRoot = defaultDataRoot
	}
	return &Worker{Store: probestore.New(bus), Prober: prober, Opts: o}
}

// Handle implements events.Handler for both lanes: decode the task, ack it
// unprobed if it is superseded or answered, probe the file, record the answer,
// ack. A KV error is returned (nak, then BackOff). A handler cancelled mid-probe
// (the pod is draining) records nothing, so the task is redelivered.
func (w *Worker) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	ctx = tracing.Extract(ctx, env)
	ctx, span := tracing.Start(ctx, "probe.Worker.Handle")
	defer span.End()
	if env == nil {
		return events.Discard("probe task has no envelope", errors.New("probe: nil envelope"))
	}
	var t schema.ProbeTask
	if err := schema.Decode(env.Schema, env.Data, &t); err != nil {
		return events.Discard("probe: undecodable ProbeTask", err)
	}
	if t.MediaFile.UID == "" {
		return events.Discard("probe: the task names no MediaFile UID", fmt.Errorf("key=%q", env.Key))
	}
	start := time.Now()
	superseded, err := w.Store.Superseded(ctx, t)
	if err != nil {
		return err
	}
	if superseded {
		observe(t.Lane, outcomeSuperseded, start)
		return nil
	}
	rec, outcome := w.probe(ctx, t)
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := w.Store.Answer(ctx, t, rec); err != nil {
		return err
	}
	observe(t.Lane, outcome, start)
	return nil
}

// probe probes t.Path and returns the answer to record. ProbeHash comes from
// the first stat, so a file that changed since the request answers with its
// new hash, which the reconciler reads as another file's record and asks again.
func (w *Worker) probe(ctx context.Context, t schema.ProbeTask) (schema.ProbeRecord, string) {
	rec := schema.ProbeRecord{Path: t.Path, ProbeHash: t.ProbeHash, ProbeVersion: mediainfo.ProbeVersion, Prober: w.Opts.Pod}
	fail := func(transient bool, outcome, format string, args ...any) (schema.ProbeRecord, string) {
		rec.State, rec.ProbedAt, rec.Transient = schema.ProbeFailed, time.Now().UTC(), transient
		rec.Failure = fmt.Sprintf(format, args...)
		return rec, outcome
	}
	if !filepath.IsAbs(t.Path) || !under(w.Opts.DataRoot, filepath.Clean(t.Path)) {
		return fail(false, outcomeFailed, "path %q is not under the data mount %s", t.Path, w.Opts.DataRoot)
	}
	before, err := os.Stat(t.Path)
	if err != nil {
		return fail(true, outcomeTransient, "stat: %v", err)
	}
	rec.ProbeHash = mediainfo.ProbeHash(t.Path, before.Size(), before.ModTime())

	pctx, cancel := context.WithTimeout(ctx, TaskTimeout)
	mi, _, perr := w.Prober.Probe(pctx, t.Path)
	cancel()
	after, serr := os.Stat(t.Path)
	switch {
	case errors.Is(perr, mediainfo.ErrProbeAbandoned):
		rec.Abandoned = true
		return fail(false, outcomeAbandoned, "the probe was abandoned after %s: %v", TaskTimeout, perr)
	case perr != nil && (errors.Is(perr, context.DeadlineExceeded) || errors.Is(perr, mediainfo.ErrIncompleteProbe)):
		return fail(true, outcomeTransient, "%v", perr)
	case perr != nil:
		return fail(false, outcomeFailed, "%v", perr)
	case serr != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()):
		return fail(true, outcomeTransient, "the file changed while it was probed")
	}
	rec.State, rec.ProbedAt, rec.MediaInfo = schema.ProbeProbed, time.Now().UTC(), mi
	return rec, outcomeProbed
}

// under reports whether path is root or inside it.
func under(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func observe(lane, outcome string, start time.Time) {
	metrics.ProbeDuration.WithLabelValues(lane, outcome).Observe(time.Since(start).Seconds())
}
