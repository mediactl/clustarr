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
	"path/filepath"
	"strings"

	"github.com/obinnaokechukwu/ffgo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/engine"
	"github.com/mediactl/clustarr/pkg/transcode/selfcheck"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// StandardProfile is a TranscodeProfile as the standard reads it (spec §5:
// quality, audio languages, the modifier policy and the container); the
// controller and the worker both build it here, so their plans agree.
func StandardProfile(name, hash string, spec transcodev1alpha1.TranscodeProfileSpec) standard.Profile {
	return standard.Profile{
		Name: name, Hash: hash, Quality: spec.QualityOrDefault(),
		Languages: spec.Audio.Languages, NeverTranscodeModifiers: spec.Policy.NeverTranscodeModifiers,
		Container: transcode.Container(spec.Container),
	}
}

// StandardTier is the tier the standard encodes on for a profile's class:
// the class's own encoder, Dolby Vision included -- the standard encodes
// its base layer like any HDR10 or HLG source, where the argv planner
// keeps Dolby Vision on libx265 (transcode.SelectTier). Auto with no class
// chosen yet is the CPU, as ProfileSpec plans it. The controller and the
// worker both pick it here, so their plans hash alike.
func StandardTier(profile transcode.ProfileSpec) transcode.Tier {
	switch profile.Hardware {
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
	profile := ProfileSpec(r.t.Profile.Spec, r.t.Profile.Hardware)
	tier := transcode.TierCPUx265
	if len(info.Video) > 0 {
		want := StandardTier(profile)
		caps, err := transcode.ProbeCapabilities(ctx, r.o.FFmpegPath)
		if err != nil {
			return encodeJob{}, retriable("squasharr worker: %w", err)
		}
		var ok bool
		if tier, ok = transcode.FallbackTier(want, caps); !ok {
			if gpuTier(want) {
				return encodeJob{}, gpuUnavailable("squasharr worker: this node's FFmpeg has no encoder for tier %s", want)
			}
			return encodeJob{}, retriable("squasharr worker: this node's FFmpeg has no encoder for tier %s", want)
		}
	}
	hw := standard.Hardware{Tier: tier}
	if r.o.limits != nil && gpuTier(tier) {
		hw.Limits = r.o.limits.forTier(ctx, r.o.FFmpegPath, tier, profile.Video)[tier]
	}
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
	r.tier = string(tier)
	part := uniquePartPath(partPath(sw.localOut, plan.Container), r.t.Job.UID, r.t.Attempt)
	log.InfoContext(ctx, "squasharr worker: planned", "engine", "ffgo", "decision", plan.Decision, "tier", tier,
		"encoder", plan.Video.Encoder, "decode", plan.Video.Decode, "reason", plan.Reason)
	durationMillis := info.Format.Duration.Milliseconds()
	return encodeJob{
		part:   part,
		encode: func(ctx context.Context) error { return r.encodeFFgo(ctx, plan, tier, local, part, durationMillis) },
		verify: func(ctx context.Context) (*transcode.Report, error) {
			return engine.Verify(ctx, local, part, plan.Expect)
		},
	}, nil
}

// partPath is the generic scratch name beside out: <stem>.part.<ext>.
func partPath(out string, c transcode.Container) string {
	ext := ".mkv"
	if c == transcode.ContainerMP4 {
		ext = ".mp4"
	}
	return strings.TrimSuffix(out, filepath.Ext(out)) + ".part" + ext
}

// openDevice opens the GPU a tier's plan decodes, filters or encodes on;
// nil for the CPU tier.
func openDevice(tier transcode.Tier) (*ffgo.HWDevice, error) {
	switch tier {
	case transcode.TierNVENC:
		return ffgo.NewHWDevice(ffgo.HWDeviceTypeCUDA, "")
	case transcode.TierQSV:
		return ffgo.NewHWDevice(ffgo.HWDeviceTypeQSV, "")
	case transcode.TierVAAPI:
		node := selfcheck.IntelRenderNode()
		if node == "" {
			return nil, errors.New("no Intel render node in this pod")
		}
		return ffgo.NewHWDevice(ffgo.HWDeviceTypeVAAPI, node)
	}
	return nil, nil
}

// encodeFFgo runs the plan on the in-process engine, with the same
// progress, telemetry, stderr tail and failure classes as the argv engine.
func (r *runner) encodeFFgo(ctx context.Context, plan standard.Result, tier transcode.Tier, local, part string, durationMillis int64) error {
	metrics.TranscodeJobsActive.WithLabelValues(r.tier).Inc()
	defer metrics.TranscodeJobsActive.WithLabelValues(r.tier).Dec()
	r.started = r.o.Now()

	dev, err := openDevice(tier)
	if err != nil {
		return gpuUnavailable("squasharr worker: open the %s device: %w", tier, err)
	}
	if dev != nil {
		defer func() { _ = dev.Close() }()
	}
	rep := newProgressReporter(r.o.ProgressInterval, durationMillis, r.o.Now, r.applyProgress)
	var pod *schema.Ref
	if r.o.PodName != "" {
		pod = &schema.Ref{Namespace: r.t.Job.Namespace, Name: r.o.PodName}
	}
	tel := newTelemetry(r.o.Telemetry, r.o.TelemetryInterval, r.t.Job, pod, durationMillis, r.o.Now)
	rep.start(ctx)
	tel.start(ctx)
	_, runErr := engine.Run(ctx, plan, local, part, engine.Options{
		HWDevice: dev,
		Progress: func(p transcode.Progress) {
			rep.observe(p)
			tel.observe(p)
		},
	})
	rep.stop(ctx)
	tel.stop(ctx)
	if p, ok := rep.last(); ok {
		r.speedMilli = p.SpeedMilli
	}
	if runErr == nil {
		return nil
	}
	var ee *engine.Error
	if errors.As(runErr, &ee) {
		r.applyStderrTail(ee.LogTail)
	}
	if gpuTier(tier) {
		return gpuEncodeFailed(fmt.Errorf("squasharr worker: engine: %w", runErr))
	}
	return retriable("squasharr worker: engine: %w", runErr)
}
