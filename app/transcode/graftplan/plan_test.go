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

package graftplan_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/graftplan"
	"github.com/mediactl/clustarr/app/transcode/grafttask"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

var (
	t0  = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	now = metav1.NewTime(t0)
)

const (
	fileName  = "perfect-blue-1997"
	target    = "/data/media/movies/Perfect Blue (1997)/Perfect Blue (1997).mkv"
	root      = "/data/media/movies"
	donorPath = "/data/media/movies/.clustarr/donors/mv-uid/perfect-blue.mka"
	release   = "Perfect.Blue.1997.DVDRip.DUAL-GRP"
)

func theDonor() *catalogv1alpha1.AudioDonor {
	return &catalogv1alpha1.AudioDonor{Path: donorPath, Release: release, DownloadRef: "pb-donor", ImportedAt: metav1.NewTime(t0.Add(-time.Hour))}
}

func theItem() *graftplan.Item {
	return &graftplan.Item{UID: "mv-uid", Anchor: "ja", Grafts: true, Default: "en",
		Audio: &catalogv1alpha1.AudioState{Wanted: []string{"en", "ja"}, Present: []string{"ja"}, Missing: []string{"en"}, Donor: theDonor()}}
}

func ready() graftplan.Input {
	return graftplan.Input{Item: theItem(), Root: root, RecycleBin: "/data/media/.recycle"}
}

func theFile() *catalogv1alpha1.MediaFile {
	return &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: fileName, Namespace: "media", UID: "mf-uid"},
		Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "perfect-blue"}, Path: target},
	}
}

// probed is a status probed a minute before t0 as hash, with these audio languages.
func probed(hash string, langs ...string) catalogv1alpha1.MediaFileStatus {
	var a []commonv1.AudioStream
	for _, l := range langs {
		a = append(a, commonv1.AudioStream{Language: l})
	}
	at := metav1.NewTime(t0.Add(-time.Minute))
	return catalogv1alpha1.MediaFileStatus{ProbeHash: hash, ProbedAt: &at, MediaInfo: &commonv1.MediaInfo{Audio: a}}
}

func with(st catalogv1alpha1.MediaFileStatus, b *catalogv1alpha1.GraftState) catalogv1alpha1.MediaFileStatus {
	st.Graft = b
	return st
}

type opt func(*graftplan.View)

func at(t time.Time) opt                             { return func(v *graftplan.View) { v.Now = metav1.NewTime(t) } }
func draft(st catalogv1alpha1.MediaFileStatus) opt   { return func(v *graftplan.View) { v.Draft = &st } }
func probePending() opt                              { return func(v *graftplan.View) { v.ProbeCurrent = false } }
func fileOpt(f func(*catalogv1alpha1.MediaFile)) opt { return func(v *graftplan.View) { f(v.File) } }

// pass runs one graft planner pass over prev; the draft is prev unless an
// opt replaces it, and dispatches issue sequences from 100.
func pass(prev catalogv1alpha1.MediaFileStatus, in graftplan.Input, opts ...opt) (catalogv1alpha1.MediaFileStatus, graftplan.Decision) {
	next := int64(99)
	v := graftplan.View{File: theFile(), Prev: &prev, Draft: prev.DeepCopy(), Now: now, ProbeCurrent: true,
		Issue: func(rec int64) int64 { next = max(next+1, rec+1); return next }}
	for _, o := range opts {
		o(&v)
	}
	out := *v.Draft.DeepCopy()
	return out, graftplan.Plan(v, in, &out)
}

func kinds(d graftplan.Decision) []graftplan.EffectKind {
	var k []graftplan.EffectKind
	for _, e := range d.Effects {
		k = append(k, e.Kind)
	}
	return k
}

func flying(phase catalogv1alpha1.GraftPhase, seq int64) *catalogv1alpha1.GraftState {
	return &catalogv1alpha1.GraftState{Phase: phase, DonorRelease: release, DonorImportedAt: &theDonor().ImportedAt,
		Languages: []string{"en"}, ProbeHash: "h1", JobName: graftplan.JobName(fileName, seq),
		Dispatch: &catalogv1alpha1.Dispatch{Seq: seq, DispatchedAt: metav1.NewTime(t0.Add(-10 * time.Minute))}}
}

func rec(seq int64, state string, ans *grafttask.Result) *grafttask.Record {
	r := &grafttask.Record{RecordHeader: schema.RecordHeader{Schema: grafttask.RecordSchema,
		MediaFile: schema.Ref{Namespace: "media", Name: fileName, UID: "mf-uid"}, Seq: seq, State: state, Writer: "graft-0"}, Answer: ans}
	switch state {
	case records.StateClaimed:
		c := t0.Add(-5 * time.Minute)
		r.ClaimedAt = &c
	case records.StateAnswered, records.StateFailed:
		a := t0.Add(-time.Minute)
		r.AnsweredAt = &a
	}
	return r
}

// TestAGraftBlockExistsOnlyWhereAGraftCan (§2.6, D10).
func TestAGraftBlockExistsOnlyWhereAGraftCan(t *testing.T) {
	cases := map[string]struct {
		in   func() graftplan.Input
		opts []opt
	}{
		"the profile does not graft":       {in: func() graftplan.Input { in := ready(); in.Item.Grafts = false; return in }},
		"the item has no donor":            {in: func() graftplan.Input { in := ready(); in.Item.Audio.Donor = nil; return in }},
		"the original language is unknown": {in: func() graftplan.Input { in := ready(); in.Item.Anchor = ""; return in }},
		"the item is gone":                 {in: func() graftplan.Input { in := ready(); in.Item = nil; return in }},
		"a multi-episode file": {in: ready, opts: []opt{fileOpt(func(mf *catalogv1alpha1.MediaFile) {
			mf.Spec.MediaRef.Keys = []string{"perfect-blue-2"}
		})}},
	}
	out, _ := pass(probed("h1", "jpn"), ready())
	require.NotNil(t, out.Graft, "the control: a single-item file whose grafting item has a donor and lacks a language")
	for name, c := range cases {
		out, d := pass(probed("h1", "jpn"), c.in(), c.opts...)
		assert.Nil(t, out.Graft, name)
		assert.Empty(t, d.Effects, name)
		gone, _ := pass(with(probed("h1", "jpn"), &catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseWaiting,
			Reason: graftplan.ReasonWaitingForSlot}), c.in(), c.opts...)
		assert.Nil(t, gone.Graft, "%s: a waiting block goes too", name)
	}
}

// TestAFileMissingTheDonorsLanguageWaitsForASlot: no transcode block, no grant.
func TestAFileMissingTheDonorsLanguageWaitsForASlot(t *testing.T) {
	out, d := pass(probed("h1", "jpn"), ready())
	require.NotNil(t, out.Graft)
	assert.Equal(t, catalogv1alpha1.GraftPhaseWaiting, out.Graft.Phase)
	assert.Equal(t, graftplan.ReasonWaitingForSlot, out.Graft.Reason)
	assert.Equal(t, release, out.Graft.DonorRelease)
	assert.Equal(t, []string{"en"}, out.Graft.Languages)
	assert.Equal(t, "h1", out.Graft.ProbeHash)
	assert.Empty(t, d.Effects)
}

// TestAGrantedGraftIsQueuedThenRecordedThenItsJobCreated (§4.12 row 1,
// §5.13 "Dispatch"): Queued with the dispatch in the status; then, in this
// order, the requested record, the Job, the ledger's confirmation.
func TestAGrantedGraftIsQueuedThenRecordedThenItsJobCreated(t *testing.T) {
	in := ready()
	in.Granted = true
	out, d := pass(probed("h1", "jpn"), in)
	require.NotNil(t, out.Graft)
	b := out.Graft
	assert.Equal(t, catalogv1alpha1.GraftPhaseQueued, b.Phase)
	require.NotNil(t, b.Dispatch)
	assert.Equal(t, int64(100), b.Dispatch.Seq)
	assert.Equal(t, now, b.Dispatch.DispatchedAt)
	assert.Equal(t, graftplan.JobName(fileName, 100), b.JobName)
	assert.Equal(t, []graftplan.EffectKind{graftplan.EffectRequest, graftplan.EffectCreateJob, graftplan.EffectConfirm}, kinds(d))
	r := d.Effects[0].Record
	assert.Equal(t, records.StateRequested, r.State)
	assert.Equal(t, int64(100), r.Seq)
	assert.Equal(t, "mf-uid", r.MediaFile.UID)
	assert.Equal(t, grafttask.Inputs{TargetProbeHash: "h1", DonorAudio: donorPath, DonorRelease: release, Language: "en", Anchor: "ja"}, r.Inputs)
	tk := d.Effects[1].Task
	assert.Equal(t, grafttask.TaskSchema, tk.Schema)
	assert.Equal(t, int64(100), tk.Seq)
	assert.Equal(t, "mv-uid", tk.Item)
	assert.Equal(t, target, tk.Target)
	assert.Equal(t, donorPath, tk.Donor)
	assert.Equal(t, root, tk.Root)
	assert.True(t, tk.Default, "the profile's default is the grafted language")
	assert.Equal(t, r.Inputs, tk.Inputs)
	assert.Equal(t, b.JobName, d.Effects[1].Job)
}

func TestTheFileCarryingTheLanguageIsPresent(t *testing.T) {
	out, d := pass(probed("h1", "jpn", "eng"), ready())
	require.NotNil(t, out.Graft)
	assert.Equal(t, catalogv1alpha1.GraftPhaseSucceeded, out.Graft.Phase)
	assert.Equal(t, grafttask.ReasonPresent, out.Graft.Reason)
	assert.Empty(t, d.Effects)
	again, _ := pass(out, ready())
	assert.Equal(t, out.Graft, again.Graft, "a Present verdict for these bytes is written once")
}

func TestAClaimedRecordIsRunning(t *testing.T) {
	in := ready()
	in.Record = rec(100, records.StateClaimed, nil)
	in.Job = &graftplan.Job{Name: graftplan.JobName(fileName, 100)}
	out, d := pass(with(probed("h1", "jpn"), flying(catalogv1alpha1.GraftPhaseQueued, 100)), in)
	assert.Equal(t, catalogv1alpha1.GraftPhaseRunning, out.Graft.Phase)
	assert.Equal(t, metav1.NewTime(t0.Add(-5*time.Minute)), *out.Graft.StartedAt)
	assert.Equal(t, "graft pod graft-0", out.Graft.Message)
	assert.Empty(t, d.Effects)
}

// TestAnAnsweredGraftSwapsThenSucceedsOnItsProbe (§4.12 "record answered"):
// Grafted is Swapping with its tag until the grafted bytes' probe lands;
// then Succeeded, and status.graftTag takes the tag.
func TestAnAnsweredGraftSwapsThenSucceedsOnItsProbe(t *testing.T) {
	in := ready()
	in.Record = rec(100, records.StateAnswered, &grafttask.Result{Phase: grafttask.PhaseSucceeded, Reason: grafttask.ReasonGrafted,
		GraftTag: "tag-1", Segments: []grafttask.Segment{{LengthMillis: 1000}}})
	out, d := pass(with(probed("h1", "jpn"), flying(catalogv1alpha1.GraftPhaseRunning, 100)), in)
	b := out.Graft
	assert.Equal(t, catalogv1alpha1.GraftPhaseSwapping, b.Phase)
	assert.Equal(t, "tag-1", b.Tag)
	assert.Equal(t, int64(100), b.Dispatch.AnsweredSeq)
	assert.Len(t, b.Segments, 1)
	assert.Equal(t, []graftplan.EffectKind{graftplan.EffectDeleteJob}, kinds(d))
	assert.Equal(t, graftplan.JobName(fileName, 100), d.Effects[0].Job)
	assert.Empty(t, out.GraftTag, "not before the grafted bytes are probed")

	still, _ := pass(out, ready())
	assert.Equal(t, catalogv1alpha1.GraftPhaseSwapping, still.Graft.Phase, "the probe of the new bytes has not landed")
	reprobed := probed("h2", "jpn", "eng")
	reprobed.Graft = out.Graft
	done, ev := pass(out, ready(), draft(reprobed))
	assert.Equal(t, catalogv1alpha1.GraftPhaseSucceeded, done.Graft.Phase)
	assert.Equal(t, "h2", done.Graft.ProbeHash)
	assert.Equal(t, "tag-1", done.GraftTag)
	assert.Equal(t, now, *done.GraftedAt)
	require.Len(t, ev.Events, 1)
	assert.Equal(t, "Grafted", ev.Events[0].Reason)
}

// TestADonorFaultFailsForGoodAndOtherFailuresBackOff (§4.12, §2.6).
func TestADonorFaultFailsForGoodAndOtherFailuresBackOff(t *testing.T) {
	in := ready()
	in.Record = rec(100, records.StateAnswered, &grafttask.Result{Phase: grafttask.PhaseFailed, Reason: grafttask.ReasonAlignmentRejected, Message: "41% aligned"})
	out, _ := pass(with(probed("h1", "jpn"), flying(catalogv1alpha1.GraftPhaseRunning, 100)), in)
	assert.Equal(t, catalogv1alpha1.GraftPhaseFailed, out.Graft.Phase)
	assert.True(t, out.Graft.DonorFault)
	granted := ready()
	granted.Granted = true
	later, d := pass(out, granted, at(t0.Add(48*time.Hour)))
	assert.Equal(t, catalogv1alpha1.GraftPhaseFailed, later.Graft.Phase, "final for this donor and these bytes")
	assert.Empty(t, d.Effects)

	in.Record = rec(100, records.StateAnswered, &grafttask.Result{Phase: grafttask.PhaseFailed, Reason: grafttask.ReasonTargetChanged})
	out, _ = pass(with(probed("h1", "jpn"), flying(catalogv1alpha1.GraftPhaseRunning, 100)), in)
	assert.Equal(t, catalogv1alpha1.GraftPhaseWaiting, out.Graft.Phase)
	assert.Equal(t, graftplan.ReasonBackoff, out.Graft.Reason)
	held, d := pass(out, granted, at(t0.Add(10*time.Minute)))
	assert.Equal(t, graftplan.ReasonBackoff, held.Graft.Reason)
	assert.Empty(t, d.Effects)
	assert.Equal(t, t0.Add(-time.Minute).Add(graftplan.FailureBackoff), d.Due)
	retried, d := pass(out, granted, at(t0.Add(20*time.Minute)))
	assert.Equal(t, catalogv1alpha1.GraftPhaseQueued, retried.Graft.Phase)
	assert.Equal(t, []graftplan.EffectKind{graftplan.EffectRequest, graftplan.EffectCreateJob, graftplan.EffectConfirm}, kinds(d))
}

// TestALostJobIsWithdrawnBeforeItFails (§4.12 "Job Complete/Failed with no
// answer after graftJobGrace", §5.10's two passes): the withdrawal fences a
// late pod; only then does the block read Failed/JobLost.
func TestALostJobIsWithdrawnBeforeItFails(t *testing.T) {
	in := ready()
	in.Record = rec(100, records.StateClaimed, nil)
	in.Job = &graftplan.Job{Name: graftplan.JobName(fileName, 100), Finished: true, FinishedAt: t0.Add(-time.Minute), Message: "DeadlineExceeded"}
	prev := with(probed("h1", "jpn"), flying(catalogv1alpha1.GraftPhaseRunning, 100))
	held, d := pass(prev, in)
	assert.Equal(t, catalogv1alpha1.GraftPhaseRunning, held.Graft.Phase, "inside graftJobGrace the answer may still land")
	assert.Empty(t, d.Effects)
	assert.Equal(t, t0.Add(-time.Minute).Add(graftplan.JobGrace), d.Due)

	in.Job.FinishedAt = t0.Add(-3 * time.Minute)
	first, d := pass(prev, in)
	assert.Equal(t, catalogv1alpha1.GraftPhaseRunning, first.Graft.Phase)
	assert.Equal(t, graftplan.ReasonJobLost, first.Graft.Reason)
	assert.Equal(t, "withdrawing attempt 100", first.Graft.Message)
	assert.Equal(t, []graftplan.EffectKind{graftplan.EffectWithdraw}, kinds(d))
	assert.Equal(t, int64(100), d.Effects[0].Seq)

	in.Record = rec(100, records.StateWithdrawn, nil)
	second, d := pass(first, in)
	b := second.Graft
	assert.Equal(t, catalogv1alpha1.GraftPhaseFailed, b.Phase)
	assert.Equal(t, graftplan.ReasonJobLost, b.Reason)
	assert.Contains(t, b.Message, "DeadlineExceeded")
	assert.Equal(t, int64(100), b.Dispatch.AnsweredSeq)
	assert.True(t, b.Dispatch.Withdrawn)
	assert.Equal(t, []graftplan.EffectKind{graftplan.EffectDeleteJob}, kinds(d))
}

// TestAGraftJobWithoutARecordFailsJobLost (§4.15): the Job finished and the
// record never answered -- the same withdraw-then-fail path.
func TestAGraftJobWithoutARecordFailsJobLost(t *testing.T) {
	in := ready()
	in.Record = rec(100, records.StateRequested, nil)
	in.Job = &graftplan.Job{Name: graftplan.JobName(fileName, 100), Finished: true, FinishedAt: t0.Add(-3 * time.Minute)}
	out, d := pass(with(probed("h1", "jpn"), flying(catalogv1alpha1.GraftPhaseQueued, 100)), in)
	assert.Equal(t, graftplan.ReasonJobLost, out.Graft.Reason)
	assert.Equal(t, []graftplan.EffectKind{graftplan.EffectWithdraw}, kinds(d))
}

// TestAFactAfterTheWithdrawalIsIncorporated (§4.10, D41).
func TestAFactAfterTheWithdrawalIsIncorporated(t *testing.T) {
	lost := flying(catalogv1alpha1.GraftPhaseFailed, 100)
	lost.Reason, lost.Dispatch.AnsweredSeq, lost.Dispatch.Withdrawn = graftplan.ReasonJobLost, 100, true
	in := ready()
	in.Record = rec(100, records.StateAnswered, &grafttask.Result{Phase: grafttask.PhaseSucceeded, Reason: grafttask.ReasonGrafted, GraftTag: "late"})
	out, _ := pass(with(probed("h1", "jpn"), lost), in)
	assert.Equal(t, catalogv1alpha1.GraftPhaseSwapping, out.Graft.Phase)
	assert.Equal(t, "late", out.Graft.Tag)
	assert.False(t, out.Graft.Dispatch.Withdrawn)

	stale, _ := pass(with(probed("h1", "jpn"), lost), in, at(t0.Add(graftplan.FactWindow)))
	assert.NotEqual(t, catalogv1alpha1.GraftPhaseSwapping, stale.Graft.Phase, "past the fact window the record is not read")
	assert.Empty(t, stale.Graft.Tag)
}

// TestAFactUnderTheNextRequestClosesItAndIsIncorporated (§4.10): attempt
// 100 was withdrawn as lost; the retry's request for 101 missed its CAS
// because 100's fact landed, so 101 never ran: it is closed without a
// record write and 100's fact incorporated. A fact the block already
// incorporated is not incorporated again under the next request: that
// request is owed again instead.
func TestAFactUnderTheNextRequestClosesItAndIsIncorporated(t *testing.T) {
	lost := flying(catalogv1alpha1.GraftPhaseFailed, 100)
	lost.Reason, lost.Dispatch.AnsweredSeq, lost.Dispatch.Withdrawn = graftplan.ReasonJobLost, 100, true
	granted := ready()
	granted.Granted = true
	granted.Record = rec(100, records.StateWithdrawn, nil)
	retried, d := pass(with(probed("h1", "jpn"), lost), granted)
	require.Equal(t, catalogv1alpha1.GraftPhaseQueued, retried.Graft.Phase)
	require.Equal(t, int64(101), retried.Graft.Dispatch.Seq)
	require.Equal(t, []graftplan.EffectKind{graftplan.EffectRequest, graftplan.EffectCreateJob, graftplan.EffectConfirm}, kinds(d))

	in := ready()
	in.Record = rec(100, records.StateAnswered, &grafttask.Result{Phase: grafttask.PhaseSucceeded, Reason: grafttask.ReasonGrafted, GraftTag: "t100"})
	out, d := pass(retried, in)
	assert.Equal(t, catalogv1alpha1.GraftPhaseSwapping, out.Graft.Phase)
	assert.Equal(t, int64(101), out.Graft.Dispatch.AnsweredSeq)
	assert.Equal(t, "t100", out.Graft.Tag)
	assert.Equal(t, []graftplan.EffectKind{graftplan.EffectDeleteJob}, kinds(d))
	assert.Equal(t, graftplan.JobName(fileName, 101), d.Effects[0].Job)

	// 100's Grafted answer incorporated (Succeeded on the grafted bytes),
	// then a new donor for another language dispatches 101, whose request
	// failed: the record still holds the incorporated fact at 100.
	done := flying(catalogv1alpha1.GraftPhaseSucceeded, 100)
	done.Reason, done.ProbeHash, done.Dispatch.AnsweredSeq = grafttask.ReasonGrafted, "h2", 100
	item := ready()
	item.Granted = true
	item.Item.Audio.Missing = []string{"fr"}
	item.Item.Audio.Donor = &catalogv1alpha1.AudioDonor{Path: donorPath, Release: "Perfect.Blue.1997.FRENCH-GRP", ImportedAt: now}
	item.Record = in.Record
	next, d := pass(with(probed("h2", "jpn", "eng"), done), item)
	require.Equal(t, catalogv1alpha1.GraftPhaseQueued, next.Graft.Phase)
	require.Equal(t, int64(101), next.Graft.Dispatch.Seq)
	require.Equal(t, []graftplan.EffectKind{graftplan.EffectRequest, graftplan.EffectCreateJob, graftplan.EffectConfirm}, kinds(d))
	item.Granted = false
	again, d := pass(next, item)
	assert.Equal(t, catalogv1alpha1.GraftPhaseQueued, again.Graft.Phase, "an incorporated fact is not incorporated again")
	assert.Empty(t, again.Graft.Tag)
	assert.Equal(t, []graftplan.EffectKind{graftplan.EffectRequest, graftplan.EffectCreateJob}, kinds(d), "101 is owed")
}

// TestOwedEffectsAreEmittedAgain (§4.7): a Queued block whose record or Job
// is missing emits them on every pass until they land.
func TestOwedEffectsAreEmittedAgain(t *testing.T) {
	granted := ready()
	granted.Granted = true
	prev, d := pass(probed("h1", "jpn"), granted)
	require.Equal(t, catalogv1alpha1.GraftPhaseQueued, prev.Graft.Phase)
	requested := d.Effects[0].Record // the record the dispatch wrote
	_, d = pass(prev, ready())
	assert.Equal(t, []graftplan.EffectKind{graftplan.EffectRequest, graftplan.EffectCreateJob}, kinds(d), "neither landed")
	assert.Equal(t, requested, d.Effects[0].Record, "the same record, again")
	in := ready()
	in.Record = requested
	_, d = pass(prev, in)
	assert.Equal(t, []graftplan.EffectKind{graftplan.EffectCreateJob}, kinds(d), "the record landed; the Job did not")
	assert.Equal(t, graftplan.JobName(fileName, 100), d.Effects[0].Job)
	assert.Equal(t, requested.Inputs, d.Effects[0].Task.Inputs, "the Job's task is rebuilt from the record's inputs")
	in.Job = &graftplan.Job{Name: graftplan.JobName(fileName, 100)}
	_, d = pass(prev, in)
	assert.Empty(t, d.Effects)
	assert.Equal(t, t0.Add(time.Minute), d.Due)
}

// TestAGraftWaitsForTheFilesTranscodeAtMostSixHours (§5.13, joinWait).
func TestAGraftWaitsForTheFilesTranscodeAtMostSixHours(t *testing.T) {
	st := probed("h1", "jpn")
	st.Transcode = &catalogv1alpha1.TranscodeState{Phase: catalogv1alpha1.TranscodePhasePlanned}
	out, d := pass(st, ready())
	assert.Equal(t, graftplan.ReasonWaitingForTranscode, out.Graft.Reason)
	assert.Equal(t, now, *out.Graft.StartedAt)
	assert.Equal(t, t0.Add(30*time.Minute), d.Due)
	granted := ready()
	granted.Granted = true
	gone, d := pass(out, granted, at(t0.Add(graftplan.JoinWait+time.Minute)))
	assert.Equal(t, catalogv1alpha1.GraftPhaseQueued, gone.Graft.Phase, "after joinWait it grafts alone")
	assert.NotEmpty(t, d.Effects)
	st.Transcode.Phase = catalogv1alpha1.TranscodePhaseRunning
	running, _ := pass(st, granted)
	assert.Equal(t, graftplan.ReasonTranscodeRunning, running.Graft.Reason, "never beside a transcode in flight")
}

// TestAJoinedGraftRunsWithItsTranscodeAndTakesItsResult (§5.13, D42).
func TestAJoinedGraftRunsWithItsTranscodeAndTakesItsResult(t *testing.T) {
	st := with(probed("h1", "jpn"), &catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseWaiting, Reason: graftplan.ReasonWaitingForSlot})
	st.Transcode = &catalogv1alpha1.TranscodeState{Phase: catalogv1alpha1.TranscodePhaseQueued,
		Dispatch:    &catalogv1alpha1.Dispatch{Seq: 200, DispatchedAt: now},
		JoinedGraft: &catalogv1alpha1.TranscodeGraftJoin{DonorRelease: release, Languages: []string{"en"}, ProbeHash: "h1"}}
	out, d := pass(st, ready())
	b := out.Graft
	assert.Equal(t, catalogv1alpha1.GraftPhaseRunning, b.Phase)
	assert.Equal(t, graftplan.ReasonJoinedTranscode, b.Reason)
	assert.Equal(t, int64(200), b.JoinedTranscodeSeq)
	assert.Empty(t, d.Effects, "a joined graft holds no slot, writes no record and makes no Job")
	assert.False(t, graftplan.Standalone(b))

	in := ready()
	in.Transcode = &task.Record{RecordHeader: schema.RecordHeader{Seq: 200, State: records.StateAnswered, AnsweredAt: &t0},
		Answer: &task.Answer{Graft: &grafttask.Result{Phase: grafttask.PhaseSucceeded, Reason: grafttask.ReasonGrafted, GraftTag: "joined-tag"}}}
	took, _ := pass(out, in)
	assert.Equal(t, catalogv1alpha1.GraftPhaseSwapping, took.Graft.Phase)
	assert.Equal(t, "joined-tag", took.Graft.Tag)
	assert.Equal(t, int64(200), took.Graft.JoinedTranscodeSeq)

	in.Transcode.Answer.Graft = nil
	without, _ := pass(out, in)
	assert.Equal(t, catalogv1alpha1.GraftPhaseWaiting, without.Graft.Phase)
	assert.Equal(t, graftplan.ReasonWaitingForSlot, without.Graft.Reason, "the transcode never grafted: graft alone")
	again, _ := pass(without, ready(), draft(st))
	assert.Equal(t, catalogv1alpha1.GraftPhaseWaiting, again.Graft.Phase, "a join already incorporated is not derived again")
}

// TestEveryPlannerKeepsItsBlockWhileTheProbeIsPending (§3.5).
func TestEveryPlannerKeepsItsBlockWhileTheProbeIsPending(t *testing.T) {
	prev := with(probed("h1", "jpn"), flying(catalogv1alpha1.GraftPhaseQueued, 100))
	prev.GraftTag = "old"
	out, d := pass(prev, ready(), probePending())
	assert.Equal(t, prev.Graft, out.Graft)
	assert.Equal(t, "old", out.GraftTag)
	assert.Empty(t, d.Effects)
}

// TestAnInFlightBlockOutlivesItsConditions (deviation 3): a donor replaced
// or a profile edited mid-flight never drops a running graft's block.
func TestAnInFlightBlockOutlivesItsConditions(t *testing.T) {
	in := ready()
	in.Item.Grafts = false
	in.Record = rec(100, records.StateClaimed, nil)
	in.Job = &graftplan.Job{Name: graftplan.JobName(fileName, 100)}
	out, _ := pass(with(probed("h1", "jpn"), flying(catalogv1alpha1.GraftPhaseRunning, 100)), in)
	require.NotNil(t, out.Graft)
	assert.Equal(t, catalogv1alpha1.GraftPhaseRunning, out.Graft.Phase)
}

func TestAFileUnderNoRootFolderFails(t *testing.T) {
	in := ready()
	in.Root = ""
	in.Granted = true
	out, d := pass(probed("h1", "jpn"), in)
	assert.Equal(t, catalogv1alpha1.GraftPhaseFailed, out.Graft.Phase)
	assert.Equal(t, grafttask.ReasonInvalidTask, out.Graft.Reason)
	assert.Empty(t, d.Effects)
}

// TestJobNameIsLabelSafeAndTheDispatchIdentity (deviation 1).
func TestJobNameIsLabelSafeAndTheDispatchIdentity(t *testing.T) {
	long := strings.Repeat("a-very-long-series-title-", 10) + "s01e01-0123456789"
	assert.LessOrEqual(t, len(graftplan.JobName(long, 1<<40)), 63)
	assert.Equal(t, graftplan.JobName(long, 7), graftplan.JobName(long, 7))
	assert.NotEqual(t, graftplan.JobName(long, 7), graftplan.JobName(long, 8))
}
