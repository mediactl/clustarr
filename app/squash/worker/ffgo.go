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
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/grafttask"
	"github.com/mediactl/clustarr/app/squash/jobspec"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// Engine is the in-process transcoder, the worker's only one:
// app/squash/worker/inprocess, which cmd/transcode supplies through
// Options.Engine. This package never imports it: cmd/clustarr imports this
// package for planning and must stay a static binary, and ffgo's purego
// would make it a dynamic one (TestClustarrNeverLinksADynamicLoader).
type Engine interface {
	// Encode runs plan from input to output on tier's device, calling
	// progress from the muxer. A failure returns FFmpeg's log tail with
	// it; a device that cannot be opened is transcode.ErrDeviceUnavailable.
	Encode(ctx context.Context, plan standard.Result, tier transcode.Tier, input, output string,
		progress func(transcode.Progress)) (logTail string, err error)
	// Verify probes output against exp and the source.
	Verify(ctx context.Context, source, output string, exp standard.Expectation) (*transcode.Report, error)
	// Measure measures this pod's device for class (spec §4): the tier it
	// encodes on and its limits, or transcode.ErrDeviceUnavailable.
	Measure(ctx context.Context, class transcode.Hardware) (transcode.Measurement, error)
	// Probe reads path in-process (pkg/mediainfo/native), as ffprobe
	// printed it.
	Probe(ctx context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error)
}

// GraftEngine is an Engine that can graft a donor's dub into a transcode in
// the same pass (anime dual-audio, phase 4 addendum): the in-process one.
type GraftEngine interface {
	// PrepareGraft readies t's graft into source (a local path) -- picks
	// the tracks, aligns the anchors, verifies the alignment -- or says why
	// not: a failure, or Succeeded/Present when source carries the language.
	PrepareGraft(ctx context.Context, source string, t grafttask.Task, dataDir string) (GraftPrepared, grafttask.Result)
	// EncodeGraft is Encode with the prepared graft's track added after the
	// plan's audio.
	EncodeGraft(ctx context.Context, plan standard.Result, tier transcode.Tier, input, output string,
		g GraftPrepared, progress func(transcode.Progress)) (logTail string, err error)
	// CheckGraft verifies the grafted track of output (audio stream
	// graftedIndex of total); a zero Result passed.
	CheckGraft(ctx context.Context, g GraftPrepared, output string, graftedIndex, total int) grafttask.Result
}

// GraftPrepared is a graft ready to mux (grafttask.Prepared).
type GraftPrepared = grafttask.Prepared

// ffgoJob plans with the standard and encodes in-process on ffgo.
func (r *runner) ffgoJob(ctx context.Context, info transcode.MediaInfo, sw swap, local string) (encodeJob, error) {
	log := logging.FromContext(ctx)
	m, err := r.measurement(ctx, jobspec.ProfileHardware(r.t.Profile.Spec, r.t.Profile.Hardware))
	if err != nil {
		return encodeJob{}, err
	}
	tier := m.Tier
	hw := standard.Hardware{Tier: tier, Limits: m.Limits}
	profile := jobspec.StandardProfile(r.t.Profile.Name, r.t.Profile.Hash, r.t.Profile.Spec)
	plan := standard.Plan(info, profile, hw)
	if plan.Decision == standard.DecisionSkip {
		return encodeJob{}, invalidSource("squasharr worker: live plan is skip (%s), not the work the controller planned", plan.Reason)
	}
	if r.t.PlanHash != "" {
		got := plan.Hash()
		trace.SpanFromContext(ctx).SetAttributes(attribute.Bool("transcode.plan_matches_recorded", got == r.t.PlanHash))
		if got != r.t.PlanHash {
			if lost := plannedHDRLost(info, profile, hw, plan, r.t.PlanHash); lost != "" {
				return encodeJob{}, retriable("squasharr worker: the source was planned as %s but its live probe reads SDR; "+
					"refusing to encode HDR as SDR (the next attempt probes again)", lost)
			}
			log.WarnContext(ctx, "squasharr worker: the plan differs from the one status.plan records",
				"planHash", r.t.PlanHash, "localPlanHash", got)
		}
	}
	// A task rendered for another container -- dispatched before the MP4
	// standard, its output <stem>.mkv -- must not get MP4 under that name
	// (final review I4): refused as retriable, so the dispatcher plans the
	// job again under the standard it runs.
	if want := "." + string(plan.Container); !strings.EqualFold(filepath.Ext(sw.localOut), want) {
		return encodeJob{}, retriable("squasharr worker: the task's output %s is not the %s the standard writes "+
			"(a task rendered under another container); the next dispatch plans it again", sw.out, plan.Container)
	}
	plan = withX265Pools(plan, r.o.Threads)
	r.tier = string(tier)
	part := uniquePartPath(partPath(sw.localOut, plan.Container), r.t.Job.UID, r.t.Attempt)
	sweepEarlierAttempts(ctx, part)
	log.InfoContext(ctx, "squasharr worker: planned", "engine", "ffgo", "decision", plan.Decision, "tier", tier,
		"encoder", plan.Video.Encoder, "decode", plan.Video.Decode, "reason", plan.Reason, "droppedSubtitles", plan.Dropped)
	durationMillis := info.Format.Duration.Milliseconds()
	graft, plan := r.prepareGraft(ctx, local, plan)
	return encodeJob{
		part: part, plan: plan,
		encode: func(ctx context.Context) error {
			if err := r.encodeFFgo(ctx, plan, tier, local, part, durationMillis, graft); err != nil {
				return err
			}
			// On stable storage before it is verified and renamed over the
			// source: a crash after the rename must not leave the library's
			// name on unwritten blocks, and a delayed write error the
			// muxer's close did not report surfaces here, while the
			// original is still the library's file.
			if err := syncPart(part); err != nil {
				removePart(ctx, part)
				return retriable("squasharr worker: the output could not be made durable: %w", err)
			}
			return nil
		},
		verify: func(ctx context.Context) (*transcode.Report, error) {
			rep, err := r.o.Engine.Verify(ctx, local, part, plan.Expect)
			if err != nil {
				return rep, err
			}
			// The plan's sidecars are part of the output (MP4 standard §4.1).
			// An empty one -- a track with no cues -- is not a fault: the
			// placement drops it.
			for _, s := range plan.Sidecars {
				if _, err := os.Stat(fsops.SidecarPath(part, s.Suffix)); err != nil {
					rep.Problems = append(rep.Problems, fmt.Sprintf("sidecar %s missing", s.Suffix))
					rep.OK = false
				}
			}
			if !rep.OK || graft == nil {
				return rep, nil
			}
			// The transcode is sound; the dub it carries must be too. A bad
			// one fails this attempt as retriable, with the graft's failure
			// reported: the next attempt carries no graft (the dispatcher
			// reads status.graft), so the transcode is never lost to it.
			res := graft.engine.CheckGraft(ctx, graft.prepared, part, graft.index, graft.index+graft.prepared.Tracks())
			if res.Phase != "" {
				res.Graft = r.t.Graft.Graft
				r.out.Graft = &res
				return nil, fmt.Errorf("the grafted dub failed its check: %s", res.Message)
			}
			done := graft.prepared.Aligned()
			done.Phase, done.Reason, done.Graft = grafttask.PhaseSucceeded, grafttask.ReasonGrafted, r.t.Graft.Graft
			r.out.Graft = &done
			return rep, nil
		},
	}, nil
}

// joinedGraft is a graft prepared to ride along with this transcode.
type joinedGraft struct {
	engine   GraftEngine
	prepared GraftPrepared
	index    int // the grafted track's index among the output's audio: after the plan's
}

// prepareGraft readies the task's graft, when it carries one, and returns
// the plan to encode: with the graft, it tags CLUSTARR_GRAFT and expects one
// more audio track. A graft that cannot be prepared is reported and the
// transcode goes on without it.
func (r *runner) prepareGraft(ctx context.Context, local string, plan standard.Result) (*joinedGraft, standard.Result) {
	if r.t.Graft == nil {
		return nil, plan
	}
	ge, ok := r.o.Engine.(GraftEngine)
	if !ok {
		res := grafttask.Failed(grafttask.ReasonError, "this worker's engine cannot graft")
		res.Graft = r.t.Graft.Graft
		r.out.Graft = &res
		return nil, plan
	}
	prepared, res := ge.PrepareGraft(ctx, local, *r.t.Graft, r.o.DataDir)
	if prepared == nil {
		logging.FromContext(ctx).InfoContext(ctx, "squasharr worker: the joined graft is not grafted",
			"reason", res.Reason, "message", res.Message)
		res.Graft = r.t.Graft.Graft
		r.out.Graft = &res
		return nil, plan
	}
	tags := map[string]string{"CLUSTARR_GRAFT": prepared.GraftTag()}
	for k, v := range plan.Tags {
		tags[k] = v
	}
	plan.Tags = tags
	plan.Expect.AudioStreams += int32(prepared.Tracks())
	return &joinedGraft{engine: ge, prepared: prepared, index: len(plan.Audio)}, plan
}

// syncPart fsyncs the encoded part file; a variable so a test can see when
// it runs and make it fail.
var syncPart = fsops.SyncFile

// hdrReadings are the HDR formats a source the controller planned as HDR
// may have had, by the plan each makes: HDR10 (PQ10 and HDR10+ plan
// alike), HLG, and Dolby Vision with an HDR10 or HLG base layer.
var hdrReadings = []commonv1.HdrFormat{
	commonv1.HdrFormatHDR10, commonv1.HdrFormatHLG10,
	commonv1.HdrFormatDolbyVisionHDR10, commonv1.HdrFormatDolbyVisionHLG,
}

// plannedHDRLost is the HDR mode ("hdr10", "hlg") the recorded plan was
// made for when the live probe reads the source lower -- an SDR encode
// where the controller's probe of the same file read HDR -- and "" when it
// does not. The task carries only the recorded plan's hash, so the source
// is planned again as each HDR reading, and the one whose hash is the
// recorded one names what the controller saw. A plan that differs for any
// other reason (the device's limits, a profile edit) matches none and is
// only logged, as before; the colour check in Verify is the last guard.
func plannedHDRLost(info transcode.MediaInfo, profile standard.Profile, hw standard.Hardware,
	live standard.Result, recorded string,
) string {
	if live.Decision != standard.DecisionEncode || live.Video.HDR != "sdr" {
		return ""
	}
	vi := int(live.Video.SourceIndex)
	if vi < 0 || vi >= len(info.Video) {
		return ""
	}
	for _, f := range hdrReadings {
		as := info
		as.Video = slices.Clone(info.Video)
		as.Video[vi].HDR.Format = f
		if p := standard.Plan(as, profile, hw); p.Video.HDR != "sdr" && p.Hash() == recorded {
			return p.Video.HDR
		}
	}
	return ""
}

// sweepEarlierAttempts removes the part files this job's earlier attempts
// left beside the output -- an attempt OOM-killed, or lost with its node,
// never reaches its own cleanup -- before this attempt writes its own.
// The caller holds this job's lease for this attempt (Serve), which fences
// every earlier one, so such a file is no longer being written. Only that
// is removed: a regular file of exactly the per-attempt form
// (fsops.ParseTranscodePart) for the same output stem and job, with a lower
// attempt number. A later attempt's, another job's (another profile's
// transcode of the same file), another source's and anything that is not a
// regular file -- a symlink is never followed -- stay; importarr's rescan
// sweeps an abandoned job's (app/import/worker/rescan). Best effort: a file
// that cannot be removed is logged, never a reason to fail the task.
func sweepEarlierAttempts(ctx context.Context, part string) {
	log := logging.FromContext(ctx)
	cur, ok := fsops.ParseTranscodePart(part)
	if !ok {
		return
	}
	dir := filepath.Dir(part)
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.WarnContext(ctx, "squasharr worker: listing the output's folder for earlier attempts' part files failed", "dir", dir, "error", err)
		return
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		p, ok := fsops.ParseTranscodePart(path)
		// The attempt's part, or one of its sidecar parts (fsops.SidecarPath).
		stemOK := p.Stem == cur.Stem || (strings.HasPrefix(p.Stem, cur.Stem+".") && fsops.SubtitleExt(p.Ext))
		if !ok || !e.Type().IsRegular() || !stemOK || p.JobUID8 != cur.JobUID8 || p.Attempt >= cur.Attempt {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.WarnContext(ctx, "squasharr worker: removing an earlier attempt's part file failed", "path", path, "error", err)
			continue
		}
		log.InfoContext(ctx, "squasharr worker: removed an earlier attempt's part file", "path", path, "attempt", p.Attempt)
	}
}

// withX265Pools sizes libx265's thread pool to the pod's threads, as the
// argv engine's X265Params does: left alone, x265 sizes it from the node's
// cores and oversubscribes a pod's CPU limit. It is applied after the plan
// hash is compared -- a pod's thread count is not part of the plan -- and
// returns a copy, so plan's own options are unchanged.
func withX265Pools(plan standard.Result, threads int32) standard.Result {
	if threads <= 0 || plan.Video.Encoder != "libx265" {
		return plan
	}
	opts := maps.Clone(plan.Video.Options)
	pools := fmt.Sprintf("pools=%d", threads)
	if p := opts["x265-params"]; p != "" {
		pools = p + ":" + pools
	}
	opts["x265-params"] = pools
	plan.Video.Options = opts
	return plan
}

// measurement is what this pod measured of its device for class: Serve's
// measurement at start (Options.Measurement), else one taken now -- a
// Process called on its own, as tests do. A device that cannot be used is
// the class unavailable on this node, as the argv engine reports it.
func (r *runner) measurement(ctx context.Context, class transcode.Hardware) (transcode.Measurement, error) {
	if m := r.o.Measurement; m != nil {
		return *m, nil
	}
	if class == "" {
		class = transcode.HardwareCPU
	}
	m, err := r.o.Engine.Measure(ctx, class)
	switch {
	case err == nil:
		return m, nil
	case class != transcode.HardwareCPU:
		return m, gpuUnavailable("squasharr worker: %w", err)
	default:
		return m, retriable("squasharr worker: %w", err)
	}
}

// partPath is the generic scratch name beside out: <stem>.part.<ext>.
func partPath(out string, c transcode.Container) string {
	ext := ".mkv"
	if c == transcode.ContainerMP4 {
		ext = ".mp4"
	}
	return strings.TrimSuffix(out, filepath.Ext(out)) + ".part" + ext
}

// encodeFFgo runs the plan on the in-process engine, with the same
// progress, telemetry, stderr tail and failure classes as the argv engine.
func (r *runner) encodeFFgo(ctx context.Context, plan standard.Result, tier transcode.Tier, local, part string, durationMillis int64,
	graft *joinedGraft,
) error {
	metrics.TranscodeJobsActive.WithLabelValues(r.tier).Inc()
	defer metrics.TranscodeJobsActive.WithLabelValues(r.tier).Dec()
	r.started = r.o.Now()

	rep := newProgressReporter(r.o.ProgressInterval, durationMillis, r.o.Now, r.applyProgress)
	var pod *schema.Ref
	if r.o.PodName != "" {
		pod = &schema.Ref{Namespace: r.t.Job.Namespace, Name: r.o.PodName}
	}
	tel := newTelemetry(r.o.Telemetry, r.o.TelemetryInterval, r.t.Job, pod, durationMillis, r.o.Now)
	rep.start(ctx)
	tel.start(ctx)
	progress := func(p transcode.Progress) {
		rep.observe(p)
		tel.observe(p)
	}
	var logTail string
	var runErr error
	if graft != nil {
		logTail, runErr = graft.engine.EncodeGraft(ctx, plan, tier, local, part, graft.prepared, progress)
	} else {
		logTail, runErr = r.o.Engine.Encode(ctx, plan, tier, local, part, progress)
	}
	rep.stop(ctx)
	tel.stop(ctx)
	if p, ok := rep.last(); ok {
		r.speedMilli = p.SpeedMilli
	}
	if runErr == nil {
		return nil
	}
	if errors.Is(runErr, transcode.ErrDeviceUnavailable) {
		return gpuUnavailable("squasharr worker: %w", runErr)
	}
	if logTail != "" {
		r.applyStderrTail(logTail)
	}
	if gpuTier(tier) {
		return gpuEncodeFailed(fmt.Errorf("squasharr worker: engine: %w", runErr))
	}
	return retriable("squasharr worker: engine: %w", runErr)
}
