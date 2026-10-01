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
	"path/filepath"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// Engine is the in-process transcoder (--worker-engine=ffgo):
// app/squash/worker/inprocess, which cmd/squasharr-worker supplies through
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
	// Probe reads path as pkg/mediainfo.Probe does, without ffprobe.
	Probe(ctx context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error)
}

// StandardProfile is a TranscodeProfile as the standard reads it (spec §5:
// quality, audio languages, the modifier policy and the container); the
// controller and the worker both build it here, so their plans agree.
func StandardProfile(name, hash string, spec transcodev1alpha1.TranscodeProfileSpec) standard.Profile {
	return standard.Profile{
		Name: name, Hash: hash, Quality: spec.QualityOrDefault(),
		Languages: spec.Audio.Languages, NeverTranscodeModifiers: spec.Policy.NeverTranscodeModifiers,
		Container:   transcode.Container(spec.Container),
		MinDuration: MinDuration(spec.Policy),
	}
}

// StandardTier is the tier the standard encodes on for a class (from
// ProfileHardware): the class's own encoder, Dolby Vision included -- the
// standard encodes its base layer like any HDR10 or HLG source. The
// controller and the worker both pick it here, so their plans hash alike.
func StandardTier(hw transcode.Hardware) transcode.Tier {
	switch hw {
	case transcode.HardwareNVIDIA:
		return transcode.TierNVENC
	case transcode.HardwareIntel:
		return transcode.TierQSV
	}
	return transcode.TierCPUx265
}

// ffgoJob plans with the standard and encodes in-process on ffgo.
func (r *runner) ffgoJob(ctx context.Context, info transcode.MediaInfo, sw swap, local string) (encodeJob, error) {
	log := logging.FromContext(ctx)
	m, err := r.measurement(ctx, ProfileHardware(r.t.Profile.Spec, r.t.Profile.Hardware))
	if err != nil {
		return encodeJob{}, err
	}
	tier := m.Tier
	hw := standard.Hardware{Tier: tier, Limits: m.Limits}
	plan := standard.Plan(info, StandardProfile(r.t.Profile.Name, r.t.Profile.Hash, r.t.Profile.Spec), hw)
	if plan.Decision == standard.DecisionSkip {
		return encodeJob{}, invalidSource("squasharr worker: live plan is skip (%s), not the work the controller planned", plan.Reason)
	}
	if r.t.PlanHash != "" {
		got := plan.Hash()
		trace.SpanFromContext(ctx).SetAttributes(attribute.Bool("transcode.plan_matches_recorded", got == r.t.PlanHash))
		if got != r.t.PlanHash {
			log.WarnContext(ctx, "squasharr worker: the plan differs from the one status.plan records",
				"planHash", r.t.PlanHash, "localPlanHash", got)
		}
	}
	plan = withX265Pools(plan, r.o.Threads)
	r.tier = string(tier)
	part := uniquePartPath(partPath(sw.localOut, plan.Container), r.t.Job.UID, r.t.Attempt)
	log.InfoContext(ctx, "squasharr worker: planned", "engine", "ffgo", "decision", plan.Decision, "tier", tier,
		"encoder", plan.Video.Encoder, "decode", plan.Video.Decode, "reason", plan.Reason)
	durationMillis := info.Format.Duration.Milliseconds()
	return encodeJob{
		part:   part,
		encode: func(ctx context.Context) error { return r.encodeFFgo(ctx, plan, tier, local, part, durationMillis) },
		verify: func(ctx context.Context) (*transcode.Report, error) {
			return r.o.Engine.Verify(ctx, local, part, plan.Expect)
		},
	}, nil
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
func (r *runner) encodeFFgo(ctx context.Context, plan standard.Result, tier transcode.Tier, local, part string, durationMillis int64) error {
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
	logTail, runErr := r.o.Engine.Encode(ctx, plan, tier, local, part, func(p transcode.Progress) {
		rep.observe(p)
		tel.observe(p)
	})
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
