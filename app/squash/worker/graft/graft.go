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

// Package graft runs one audio graft in a graft Job's pod (anime dual-audio
// spec §7.2): reduce the donor to its audio, align its anchor track to the
// target's (pkg/audioalign), mux the dub in beside every copied stream of
// the target (pkg/transcode/engine), verify the muxed track, and swap the
// result into place as a transcode swaps. It links FFmpeg through the
// engine, so only cmd/squasharr-worker imports it.
package graft

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/text/language"

	"github.com/mediactl/clustarr/app/squash/grafttask"
	"github.com/mediactl/clustarr/app/squash/worker"
	"github.com/mediactl/clustarr/pkg/audioalign"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/transcode/engine"
)

// Verify thresholds (spec §7.2): the median residual of the anchors after
// the transform, and the share of windows within 80 ms.
const (
	maxResidual  = 40 * time.Millisecond
	minWithin80  = 0.9
	minMuxCorr   = 0.8
	muxLagWindow = 320 // samples at 8 kHz: ±40 ms
)

// Options tune a Run.
type Options struct {
	// DataDir is where /data is mounted (worker.LogicalDataRoot when empty).
	DataDir string
	// Thresholds is the alignment's acceptance (audioalign.DefaultThresholds
	// when zero).
	Thresholds audioalign.Thresholds
}

// Run performs t and reports what happened. The target is untouched unless
// the Result is Succeeded with reason Grafted.
func Run(ctx context.Context, t grafttask.Task, o Options) grafttask.Result {
	if err := t.Validate(); err != nil {
		return grafttask.Failed(grafttask.ReasonInvalidTask, "%v", err)
	}
	if o.DataDir == "" {
		o.DataDir = worker.LogicalDataRoot
	}
	if o.Thresholds == (audioalign.Thresholds{}) {
		o.Thresholds = audioalign.DefaultThresholds
	}
	r := &run{t: t, o: o, log: logging.FromContext(ctx).With("graft", t.Graft)}
	res := r.do(ctx)
	if res.Phase == grafttask.PhaseFailed && r.part != "" {
		_ = os.Remove(r.part)
	}
	return res
}

type run struct {
	t    grafttask.Task
	o    Options
	log  interface{ Info(string, ...any) }
	part string
}

func (r *run) local(p string) (string, error) {
	if !worker.Within(r.t.Root, p) {
		return "", fmt.Errorf("%s is outside root folder %s", p, r.t.Root)
	}
	return worker.LocalPath(r.o.DataDir, p)
}

func (r *run) do(ctx context.Context) grafttask.Result {
	target, err := r.local(r.t.Target)
	if err != nil {
		return grafttask.Failed(grafttask.ReasonInvalidTask, "target: %v", err)
	}
	donor, err := r.local(r.t.Donor)
	if err != nil {
		return grafttask.Failed(grafttask.ReasonInvalidTask, "donor: %v", err)
	}
	st, err := os.Stat(target)
	if err != nil {
		return grafttask.Failed(grafttask.ReasonTargetChanged, "target %s: %v", r.t.Target, err)
	}
	if mediainfo.ProbeHash(r.t.Target, st.Size(), st.ModTime()) != r.t.TargetProbeHash {
		return grafttask.Failed(grafttask.ReasonTargetChanged, "target %s changed since the graft was planned", r.t.Target)
	}
	plan, err := engine.CopyPlan(target)
	if err != nil {
		return grafttask.Failed(grafttask.ReasonUnsupported, "%v", err)
	}

	targetTracks, err := engine.AudioTracks(target)
	if err != nil {
		return grafttask.Failed(grafttask.ReasonError, "read the target's tracks: %v", err)
	}
	if pick(targetTracks, r.t.Language, false) >= 0 {
		return grafttask.Result{Phase: grafttask.PhaseSucceeded, Reason: grafttask.ReasonPresent,
			Message: "the target already carries " + r.t.Language}
	}
	tAnchor := pick(targetTracks, r.t.Anchor, true)
	if tAnchor < 0 {
		return grafttask.Failed(grafttask.ReasonTargetLacksAnchor, "the target has no %s track (and not one untagged track)", r.t.Anchor)
	}

	mka, res, ok := r.reduceDonor(ctx, donor)
	if !ok {
		return res
	}
	donorTracks, err := engine.AudioTracks(mka)
	if err != nil {
		return grafttask.Failed(grafttask.ReasonError, "read the donor's tracks: %v", err)
	}
	dAnchor, dLang := pick(donorTracks, r.t.Anchor, false), pick(donorTracks, r.t.Language, false)
	if dAnchor < 0 || dLang < 0 {
		return grafttask.Failed(grafttask.ReasonDonorLacksLanguage, "the donor needs tagged %s and %s tracks; it has %s",
			r.t.Anchor, r.t.Language, describe(donorTracks))
	}

	// Align the anchors.
	tPCM, err := engine.DecodePCM(ctx, target, tAnchor, audioalign.SampleRate)
	if err != nil {
		return grafttask.Failed(grafttask.ReasonError, "decode the target's anchor: %v", err)
	}
	dPCM, err := engine.DecodePCM(ctx, mka, dAnchor, audioalign.SampleRate)
	if err != nil {
		return grafttask.Failed(grafttask.ReasonError, "decode the donor's anchor: %v", err)
	}
	al, err := audioalign.Align(dPCM, tPCM)
	out := alignmentResult(al)
	if err == nil {
		err = al.Accept(r.o.Thresholds)
	}
	if err != nil {
		out.Phase, out.Reason, out.Message = grafttask.PhaseFailed, grafttask.ReasonAlignmentRejected, grafttask.Clamp(err.Error())
		return out
	}
	resid, within := audioalign.Verify(dPCM, tPCM, al)
	out.ResidualMillis, out.Within80Percent = clampMillis(resid), int32(math.Round(100*within))
	if resid > maxResidual || within < minWithin80 {
		out.Phase, out.Reason = grafttask.PhaseFailed, grafttask.ReasonVerifyFailed
		out.Message = fmt.Sprintf("the transformed anchor misses by %s median, %.0f%% within 80 ms", resid, 100*within)
		return out
	}
	r.log.Info("graft: aligned", "rate", al.RateName, "segments", len(al.Segments), "coverage", al.Coverage)

	// Mux.
	tag, err := fileTag(mka)
	if err != nil {
		return grafttask.Failed(grafttask.ReasonError, "hash the donor: %v", err)
	}
	out.GraftTag, out.DonorAudio = tag, strings.TrimSuffix(r.t.Donor, filepath.Ext(r.t.Donor))+".mka"
	plan.Tags = map[string]string{"CLUSTARR_GRAFT": tag}
	r.part = strings.TrimSuffix(target, filepath.Ext(target)) + ".part" + filepath.Ext(target)
	_ = os.Remove(r.part)
	if _, err := engine.Run(ctx, plan, target, r.part, engine.Options{Graft: &engine.GraftAudio{
		Donor: mka, Stream: dLang, Map: al.DonorSeconds,
		Language: iso639(r.t.Language), Title: trackTitle(r.t.Language), Default: r.t.Default,
	}}); err != nil {
		return failedWith(out, grafttask.ReasonMuxFailed, "mux: %v", err)
	}
	if err := fsops.SyncFile(r.part); err != nil {
		return failedWith(out, grafttask.ReasonMuxFailed, "sync: %v", err)
	}

	// Verify the muxed track: it must be the donor's, moved by the
	// alignment, on the target's clock.
	grafted := len(targetTracks)
	partTracks, err := engine.AudioTracks(r.part)
	if err != nil || len(partTracks) != grafted+1 {
		return failedWith(out, grafttask.ReasonVerifyFailed, "the output has %d audio tracks, not %d (%v)", len(partTracks), grafted+1, err)
	}
	got, err := engine.DecodePCM(ctx, r.part, grafted, audioalign.SampleRate)
	if err != nil {
		return failedWith(out, grafttask.ReasonVerifyFailed, "decode the grafted track: %v", err)
	}
	dubPCM, err := engine.DecodePCM(ctx, mka, dLang, audioalign.SampleRate)
	if err != nil {
		return failedWith(out, grafttask.ReasonError, "decode the donor's %s: %v", r.t.Language, err)
	}
	want := audioalign.Transform(dubPCM, al, len(tPCM))
	if lag, corr := muxCheck(want, got); corr < minMuxCorr || abs(lag) > 8 {
		return failedWith(out, grafttask.ReasonVerifyFailed, "the grafted track is off by %d samples (correlation %.2f)", lag, corr)
	}

	// Swap, as a transcode swaps: only if the target is still the file
	// the graft was planned for.
	if st2, err := os.Stat(target); err != nil || mediainfo.ProbeHash(r.t.Target, st2.Size(), st2.ModTime()) != r.t.TargetProbeHash {
		return failedWith(out, grafttask.ReasonTargetChanged, "target %s changed during the graft", r.t.Target)
	}
	if r.t.RecycleBin != "" {
		bin, err := worker.LocalPath(r.o.DataDir, r.t.RecycleBin)
		if err != nil {
			return failedWith(out, grafttask.ReasonInvalidTask, "recycle bin: %v", err)
		}
		if _, err := fsops.RecycleLink(bin, target); err != nil {
			return failedWith(out, grafttask.ReasonError, "recycle the target: %v", err)
		}
	}
	if err := fsops.MoveAtomic(r.part, target); err != nil {
		return failedWith(out, grafttask.ReasonError, "replace the target: %v", err)
	}
	r.part = ""
	if st3, err := os.Stat(target); err == nil {
		out.OutputSizeBytes = st3.Size()
	}
	out.Phase, out.Reason = grafttask.PhaseSucceeded, grafttask.ReasonGrafted
	r.log.Info("graft: swapped", "target", r.t.Target, "tag", tag)
	return out
}

// reduceDonor is the donor's audio as <stem>.mka: the donor itself when it
// is one, else its audio tracks extracted there and the donor removed.
func (r *run) reduceDonor(ctx context.Context, donor string) (string, grafttask.Result, bool) {
	if strings.EqualFold(filepath.Ext(donor), ".mka") {
		return donor, grafttask.Result{}, true
	}
	mka := strings.TrimSuffix(donor, filepath.Ext(donor)) + ".mka"
	if exists(mka) && !exists(donor) {
		return mka, grafttask.Result{}, true // reduced by an earlier run
	}
	tracks, err := engine.AudioTracks(donor)
	if err != nil {
		return "", grafttask.Failed(grafttask.ReasonError, "read the donor's tracks: %v", err), false
	}
	var keep []int
	for _, l := range []string{r.t.Anchor, r.t.Language} {
		if i := pick(tracks, l, false); i >= 0 {
			keep = append(keep, i)
		}
	}
	if len(keep) < 2 {
		return "", grafttask.Failed(grafttask.ReasonDonorLacksLanguage, "the donor needs tagged %s and %s tracks; it has %s",
			r.t.Anchor, r.t.Language, describe(tracks)), false
	}
	part := strings.TrimSuffix(donor, filepath.Ext(donor)) + ".part.mka"
	if err := engine.ExtractAudio(ctx, donor, part, keep); err != nil {
		return "", grafttask.Failed(grafttask.ReasonError, "extract the donor's audio: %v", err), false
	}
	if err := fsops.MoveAtomic(part, mka); err != nil {
		_ = os.Remove(part)
		return "", grafttask.Failed(grafttask.ReasonError, "place the donor's audio: %v", err), false
	}
	if err := os.Remove(donor); err != nil && !errors.Is(err, os.ErrNotExist) {
		r.log.Info("graft: the donor's source stays", "err", err)
	}
	return mka, grafttask.Result{}, true
}

// pick is the first track in language l (BCP-47 base compared); with
// untagged, a lone track with no language counts (an untagged track is the
// original, as status.audio reads it).
func pick(tracks []engine.Track, l string, untagged bool) int {
	want, ok := lang.Normalize(l)
	if !ok {
		return -1
	}
	base := func(t lang.Tag) string { b, _, _ := strings.Cut(string(t), "-"); return strings.ToLower(b) }
	for _, tr := range tracks {
		if got, ok := lang.Normalize(tr.Language); ok && base(got) == base(want) {
			return tr.Index
		}
	}
	if untagged && len(tracks) == 1 {
		if _, ok := lang.Normalize(tracks[0].Language); !ok {
			return tracks[0].Index
		}
	}
	return -1
}

func describe(tracks []engine.Track) string {
	var s []string
	for _, t := range tracks {
		s = append(s, cmpOr(t.Language, "untagged"))
	}
	if len(s) == 0 {
		return "no audio"
	}
	return strings.Join(s, ", ")
}

func cmpOr(a, b string) string {
	if a == "" {
		return b
	}
	return a
}

// iso639 is the three-letter code a Matroska or MP4 track is tagged with.
func iso639(l string) string {
	t, err := language.Parse(l)
	if err != nil {
		return l
	}
	b, _ := t.Base()
	return b.ISO3()
}

// trackTitle is the grafted track's title: "English (dub, grafted)".
func trackTitle(l string) string {
	name, ok := catalogue.LanguageName(l)
	if !ok {
		name = l
	}
	return name + " (dub, grafted)"
}

// fileTag is CLUSTARR_GRAFT: the first 12 hex digits of the donor audio's
// SHA-256.
func fileTag(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

func alignmentResult(al audioalign.Result) grafttask.Result {
	out := grafttask.Result{
		RateName: al.RateName, RateMicros: int64(math.Round(al.Rate * 1e6)),
		RateMarginMilli: int32(math.Round(math.Min(al.RateMargin, 1e6) * 1000)),
		CoveragePercent: int32(math.Round(100 * al.Coverage)),
	}
	for i, s := range al.Segments {
		if i == 16 {
			break
		}
		out.Segments = append(out.Segments, grafttask.Segment{
			DonorStartMillis: s.DonorStart.Milliseconds(), TargetStartMillis: s.TargetStart.Milliseconds(), LengthMillis: s.Length.Milliseconds(),
		})
	}
	return out
}

func failedWith(out grafttask.Result, reason, format string, args ...any) grafttask.Result {
	out.Phase, out.Reason, out.Message = grafttask.PhaseFailed, reason, grafttask.Clamp(fmt.Sprintf(format, args...))
	return out
}

func clampMillis(d time.Duration) int32 {
	return int32(min(d.Milliseconds(), math.MaxInt32))
}

// muxCheck is the lag (samples, ±muxLagWindow) at which got best matches
// want, and the normalised correlation there, over up to six 10-second
// spans spread through the file where want has sound.
func muxCheck(want, got []float32) (int, float64) {
	n := min(len(want), len(got))
	const span = 10 * audioalign.SampleRate
	if n < span+2*muxLagWindow {
		return 0, 0
	}
	starts := []int{}
	for i := range 6 {
		s := muxLagWindow + i*(n-span-2*muxLagWindow)/6
		starts = append(starts, s)
	}
	best, bestC := 0, -2.0
	for lag := -muxLagWindow; lag <= muxLagWindow; lag++ {
		var xy, xx, yy float64
		for _, s := range starts {
			for i := s; i < s+span; i++ {
				a, b := float64(want[i]), float64(got[i+lag])
				xy += a * b
				xx += a * a
				yy += b * b
			}
		}
		if xx == 0 || yy == 0 {
			continue
		}
		if c := xy / math.Sqrt(xx*yy); c > bestC {
			best, bestC = lag, c
		}
	}
	return best, bestC
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
