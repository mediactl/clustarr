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
	"slices"
	"strings"
	"time"

	"golang.org/x/text/language"

	"github.com/mediactl/clustarr/app/squash/grafttask"
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
	// DataDir is where /data is mounted (grafttask.LogicalDataRoot when empty).
	DataDir string
	// Thresholds is the alignment's acceptance (audioalign.DefaultThresholds
	// when zero).
	Thresholds audioalign.Thresholds
}

// Run performs t and reports what happened: a reduce (t.Mode reduce)
// leaves the donor's audio as <stem>.mka; a graft muxes the dub into the
// target, which is untouched unless the Result is Succeeded with reason
// Grafted.
func Run(ctx context.Context, t grafttask.Task, o Options) grafttask.Result {
	if t.Mode == grafttask.ModeReduce {
		return Reduce(ctx, t, o)
	}
	if err := t.Validate(); err != nil {
		return grafttask.Failed(grafttask.ReasonInvalidTask, "%v", err)
	}
	r := newRun(ctx, t, o)
	res := r.do(ctx)
	if res.Phase == grafttask.PhaseFailed && r.part != "" {
		_ = os.Remove(r.part)
	}
	return res
}

func newRun(ctx context.Context, t grafttask.Task, o Options) *run {
	if o.DataDir == "" {
		o.DataDir = grafttask.LogicalDataRoot
	}
	if o.Thresholds == (audioalign.Thresholds{}) {
		o.Thresholds = audioalign.DefaultThresholds
	}
	return &run{t: t, o: o, log: logging.FromContext(ctx).With("graft", t.Graft)}
}

// Reduce reduces t's donor to its audio, the anchor and every language the
// AudioGraft wants: the reduce Job a new donor gets at once, so its video
// leaves the disk however long the graft itself waits.
func Reduce(ctx context.Context, t grafttask.Task, o Options) grafttask.Result {
	if t.Donor == "" || t.Root == "" || t.Language == "" || t.Anchor == "" {
		return grafttask.Failed(grafttask.ReasonInvalidTask, "grafttask: a reduce needs a donor, a root, a language and an anchor")
	}
	r := newRun(ctx, t, o)
	donor, err := r.local(t.Donor)
	if err != nil {
		return grafttask.Failed(grafttask.ReasonInvalidTask, "donor: %v", err)
	}
	if _, res, ok := r.reduceDonor(ctx, donor); !ok {
		return res
	}
	return grafttask.Result{Phase: grafttask.PhaseSucceeded, Reason: grafttask.ReasonReduced, DonorAudio: reducedPath(t.Donor)}
}

// reducedPath is where a donor's audio lives once reduced.
func reducedPath(donor string) string {
	if strings.EqualFold(filepath.Ext(donor), ".mka") {
		return donor
	}
	return strings.TrimSuffix(donor, filepath.Ext(donor)) + ".mka"
}

type run struct {
	t    grafttask.Task
	o    Options
	log  interface{ Info(string, ...any) }
	part string
}

func (r *run) local(p string) (string, error) {
	if !grafttask.Within(r.t.Root, p) {
		return "", fmt.Errorf("%s is outside root folder %s", p, r.t.Root)
	}
	return grafttask.LocalPath(r.o.DataDir, p)
}

// Prepared is a graft aligned and ready to mux: what engine.Options.Graft
// takes, and what Check verifies an output against.
type Prepared struct {
	// Result carries the alignment's figures, the graft tag and the donor
	// audio, for the result the mux finishes.
	Result grafttask.Result
	audio  engine.GraftAudio
	al     audioalign.Result
	target int // the target anchor's length at 8 kHz
	mka    string
	dub    int
	lang   string
}

// Audio is the graft as engine.Options.Graft takes it.
func (p *Prepared) Audio() *engine.GraftAudio { a := p.audio; return &a }

// GraftTag is the CLUSTARR_GRAFT tag the output carries.
func (p *Prepared) GraftTag() string { return p.Result.GraftTag }

// Prepare readies t's graft into source (a local path): it picks the
// tracks, reduces the donor if it is not yet, decodes and aligns the
// anchors and verifies the alignment. A nil Prepared comes with why: a
// failure, or Succeeded/Present when source already carries the language.
func Prepare(ctx context.Context, t grafttask.Task, o Options, source string) (*Prepared, grafttask.Result) {
	r := newRun(ctx, t, o)
	return r.prepare(ctx, source)
}

func (r *run) prepare(ctx context.Context, target string) (*Prepared, grafttask.Result) {
	donor, err := r.local(r.t.Donor)
	if err != nil {
		return nil, grafttask.Failed(grafttask.ReasonInvalidTask, "donor: %v", err)
	}
	targetTracks, err := engine.AudioTracks(target)
	if err != nil {
		return nil, grafttask.Failed(grafttask.ReasonError, "read the target's tracks: %v", err)
	}
	if pick(targetTracks, r.t.Language, false) >= 0 {
		return nil, grafttask.Result{
			Phase: grafttask.PhaseSucceeded, Reason: grafttask.ReasonPresent,
			Message: "the target already carries " + r.t.Language,
		}
	}
	tAnchor := pick(targetTracks, r.t.Anchor, true)
	if tAnchor < 0 {
		return nil, grafttask.Failed(grafttask.ReasonTargetLacksAnchor, "the target has no %s track (and not one untagged track)", r.t.Anchor)
	}
	mka, res, ok := r.reduceDonor(ctx, donor)
	if !ok {
		return nil, res
	}
	donorTracks, err := engine.AudioTracks(mka)
	if err != nil {
		return nil, grafttask.Failed(grafttask.ReasonError, "read the donor's tracks: %v", err)
	}
	dAnchor, dLang := pick(donorTracks, r.t.Anchor, false), pick(donorTracks, r.t.Language, false)
	if dAnchor < 0 || dLang < 0 {
		return nil, grafttask.Failed(grafttask.ReasonDonorLacksLanguage, "the donor needs tagged %s and %s tracks; it has %s",
			r.t.Anchor, r.t.Language, describe(donorTracks))
	}

	tPCM, err := engine.DecodePCM(ctx, target, tAnchor, audioalign.SampleRate)
	if err != nil {
		return nil, grafttask.Failed(grafttask.ReasonError, "decode the target's anchor: %v", err)
	}
	dPCM, err := engine.DecodePCM(ctx, mka, dAnchor, audioalign.SampleRate)
	if err != nil {
		return nil, grafttask.Failed(grafttask.ReasonError, "decode the donor's anchor: %v", err)
	}
	al, err := audioalign.Align(dPCM, tPCM)
	out := alignmentResult(al)
	if err == nil {
		err = al.Accept(r.o.Thresholds)
	}
	if err != nil {
		out.Phase, out.Reason, out.Message = grafttask.PhaseFailed, grafttask.ReasonAlignmentRejected, grafttask.Clamp(err.Error())
		return nil, out
	}
	resid, within := audioalign.Verify(dPCM, tPCM, al)
	out.ResidualMillis, out.Within80Percent = clampMillis(resid), int32(math.Round(100*within))
	if resid > maxResidual || within < minWithin80 {
		out.Phase, out.Reason = grafttask.PhaseFailed, grafttask.ReasonVerifyFailed
		out.Message = fmt.Sprintf("the transformed anchor misses by %s median, %.0f%% within 80 ms", resid, 100*within)
		return nil, out
	}
	r.log.Info("graft: aligned", "rate", al.RateName, "segments", len(al.Segments), "coverage", al.Coverage)
	tag, err := fileTag(mka)
	if err != nil {
		return nil, grafttask.Failed(grafttask.ReasonError, "hash the donor: %v", err)
	}
	out.GraftTag, out.DonorAudio = tag, reducedPath(r.t.Donor)
	return &Prepared{
		Result: out, al: al, target: len(tPCM), mka: mka, dub: dLang, lang: r.t.Language,
		audio: engine.GraftAudio{
			Donor: mka, Stream: dLang, Map: al.DonorSeconds,
			Language: iso639(r.t.Language), Title: trackTitle(r.t.Language), Default: r.t.Default,
		},
	}, grafttask.Result{}
}

// Check verifies the grafted track of output, its audio stream graftedIndex
// with total audio streams in all: it must be the donor's dub, moved by the
// alignment, on the target's clock. A zero Result means it passed.
func Check(ctx context.Context, p *Prepared, output string, graftedIndex, total int) grafttask.Result {
	out := p.Result
	tracks, err := engine.AudioTracks(output)
	if err != nil || len(tracks) != total {
		return failedWith(out, grafttask.ReasonVerifyFailed, "the output has %d audio tracks, not %d (%v)", len(tracks), total, err)
	}
	got, err := engine.DecodePCM(ctx, output, graftedIndex, audioalign.SampleRate)
	if err != nil {
		return failedWith(out, grafttask.ReasonVerifyFailed, "decode the grafted track: %v", err)
	}
	dubPCM, err := engine.DecodePCM(ctx, p.mka, p.dub, audioalign.SampleRate)
	if err != nil {
		return failedWith(out, grafttask.ReasonError, "decode the donor's %s: %v", p.lang, err)
	}
	want := audioalign.Transform(dubPCM, p.al, p.target)
	if lag, corr := muxCheck(want, got); corr < minMuxCorr || abs(lag) > 8 {
		return failedWith(out, grafttask.ReasonVerifyFailed, "the grafted track is off by %d samples (correlation %.2f)", lag, corr)
	}
	return grafttask.Result{}
}

func (r *run) do(ctx context.Context) grafttask.Result {
	target, err := r.local(r.t.Target)
	if err != nil {
		return grafttask.Failed(grafttask.ReasonInvalidTask, "target: %v", err)
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
	p, res := r.prepare(ctx, target)
	if p == nil {
		return res
	}
	out := p.Result

	plan.Tags = map[string]string{"CLUSTARR_GRAFT": p.GraftTag()}
	r.part = strings.TrimSuffix(target, filepath.Ext(target)) + ".part" + filepath.Ext(target)
	_ = os.Remove(r.part)
	if _, err := engine.Run(ctx, plan, target, r.part, engine.Options{Graft: p.Audio()}); err != nil {
		return failedWith(out, grafttask.ReasonMuxFailed, "mux: %v", err)
	}
	if err := fsops.SyncFile(r.part); err != nil {
		return failedWith(out, grafttask.ReasonMuxFailed, "sync: %v", err)
	}
	if res := Check(ctx, p, r.part, len(plan.Audio), len(plan.Audio)+1); res.Phase != "" {
		return res
	}

	// Swap, as a transcode swaps: only if the target is still the file
	// the graft was planned for.
	if st2, err := os.Stat(target); err != nil || mediainfo.ProbeHash(r.t.Target, st2.Size(), st2.ModTime()) != r.t.TargetProbeHash {
		return failedWith(out, grafttask.ReasonTargetChanged, "target %s changed during the graft", r.t.Target)
	}
	if r.t.RecycleBin != "" {
		bin, err := grafttask.LocalPath(r.o.DataDir, r.t.RecycleBin)
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
	r.log.Info("graft: swapped", "target", r.t.Target, "tag", p.GraftTag())
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
	// The anchor and every language the AudioGraft wants, so a later
	// graft can take another from the same donor; the language of this run
	// is required.
	var keep []int
	for _, l := range append([]string{r.t.Anchor, r.t.Language}, r.t.Languages...) {
		if i := pick(tracks, l, false); i >= 0 && !slices.Contains(keep, i) {
			keep = append(keep, i)
		}
	}
	if pick(tracks, r.t.Anchor, false) < 0 || pick(tracks, r.t.Language, false) < 0 {
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
