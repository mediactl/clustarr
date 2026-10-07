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

package subtitleplan

import (
	"cmp"
	"fmt"
	"hash/fnv"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/datapath"
	"github.com/mediactl/clustarr/app/caption/providerset"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitlestore"
)

// maxLastError is SubtitleItemStatus.lastError's MaxLength.
const maxLastError = schema.MaxSubtitleLastError

// maxPacerJitter bounds the jitter a paced-out file adds to its slot.
const maxPacerJitter = 5 * time.Second

// The Events a forced search records (loop spec §6.5, §2.9).
const (
	// EventInvalidAnnotation: subtitle.clustarr.io/search holds a value
	// that is not a nonce; it is recorded at once and warned about once.
	EventInvalidAnnotation = "InvalidAnnotation"
	// EventNothingToSearch: a forced search found no wanted language and no
	// upgrade candidate.
	EventNothingToSearch = "NothingToSearch"
)

// nonceFormatText is §2.9's nonce format, for the Warning.
const nonceFormatText = "^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$"

// Plan is one plan of the file's subtitles (§6.4). issue is View.Issue:
// the next sequence for a dispatch whose record holds recordSeq.
func Plan(in Input, issue func(recordSeq int64) int64) Decision {
	p := &pass{in: in, issue: issue}
	p.run()
	return p.d
}

// Needs runs the same pass as Plan, recording the records it would read,
// whether it would reserve a pacer slot and whether it would list, so the
// two never disagree about what is due.
func Needs(in Input) Need {
	p := &pass{in: in, issue: func(int64) int64 { return 0 }, need: &Need{}}
	p.run()
	slices.Sort(p.need.Records)
	p.need.Records = slices.Compact(p.need.Records)
	return *p.need
}

type pass struct {
	in        Input
	issue     func(int64) int64
	need      *Need // set in a Needs run
	d         Decision
	winner    *subtitlev1alpha1.SubtitleProfile
	cad       cadence
	threshold int32
	want      map[string]bool
	extract   map[int32]bool
	throttled map[string]time.Time
	wake      time.Time
	// forced: a pending subtitle.clustarr.io/search nonce is honoured this
	// pass; forcedAny: it dispatched at least one language.
	forced, forcedAny bool
}

// record is langKey's record when Gather read it; a Needs run asks for it.
func (p *pass) record(langKey string) (subtitlestore.Entry, bool) {
	if p.need != nil {
		p.need.Records = append(p.need.Records, langKey)
	}
	e, ok := p.in.Records[langKey]
	return e, ok
}

func (p *pass) askList() {
	if p.need != nil {
		p.need.List = true
	}
}

func (p *pass) askReserve() {
	if p.need != nil {
		p.need.Reserve = true
	}
}

// at lowers the pass's wake to t.
func (p *pass) at(t time.Time) {
	if !t.IsZero() && (p.wake.IsZero() || t.Before(p.wake)) {
		p.wake = t
	}
}

func (p *pass) run() {
	in, mf := p.in, p.in.File
	st := &mf.Status
	if !EligibleKind(mf.Spec.MediaRef.Kind) {
		return // the adapter's Applies keeps this unreachable
	}
	p.d.FileUID = string(mf.UID)
	p.d.Block = st.Subtitles.DeepCopy()
	p.d.Sidecars = carriedSidecars(mf.Spec.Path, st.Sidecars)
	if st.HandledNonces != nil {
		p.d.HandledNonce = st.HandledNonces.SubtitleSearch
	}
	switch {
	case !in.Item.Exists:
		p.block(ReasonItemNotFound, fmt.Sprintf("the %s %q this file belongs to no longer exists; "+
			"subtitles are not fetched for a file Clustarr no longer manages", mf.Spec.MediaRef.Kind, mf.Spec.MediaRef.Name), time.Time{})
		return
	case !in.ProbeCurrent:
		p.d.Held = true // §6.4.1: the previous block stays verbatim
		return
	}
	switch pending, invalid := p.force(); {
	case pending && invalid:
		// Recorded at once, warned once: there is nothing to honour (§2.9).
		p.d.HandledNonce = catalogv1alpha1.HandledNonce(in.ForceNonce)
		p.d.Events = append(p.d.Events, Event{
			Type: "Warning", Reason: EventInvalidAnnotation,
			Message: clip(fmt.Sprintf("%s=%q is not a nonce (%s)", catalogv1alpha1.AnnotationSubtitleSearch, in.ForceNonce, nonceFormatText), 1024),
		})
	case pending:
		p.forced = true // honoured by plan(); a Blocked file leaves it pending
	}
	if p.winner = WinningProfile(in.Labels, in.Profiles); p.winner == nil {
		p.block(ReasonNoProfile, "no SubtitleProfile selects this MediaFile and there is no default profile", time.Time{})
		return
	}
	if Invalid(p.winner) {
		p.block(ReasonProfileInvalid, fmt.Sprintf("SubtitleProfile %q is marked Invalid", p.winner.Name), time.Time{})
		return
	}
	local, err := datapath.Local(in.DataDir, mf.Spec.Path)
	if err != nil {
		p.block(ReasonNotOnDataVolume, fmt.Sprintf("MediaFile %q: %v", mf.Name, err), time.Time{})
		return
	}
	if p.listGateOpen() || p.forced {
		p.askList() // forced dispatches list first (§6.5): a hand-placed sidecar stops a search
	}
	if l := in.Listing; l != nil {
		dir, base := filepath.Dir(local), filepath.Base(mf.Spec.Path)
		switch {
		case l.Err != nil:
			p.block(ReasonMediaDirUnreadable, fmt.Sprintf("cannot list %s: %v", dir, l.Err), in.Now.Add(diskRetry))
			return
		case !slices.Contains(l.Names, base):
			// A listing without the video is what a missing mount looks
			// like too: it is not trusted about sidecars.
			p.block(ReasonNotOnDisk, fmt.Sprintf("%s is not in %s", base, dir), in.Now.Add(diskRetry))
			return
		}
		p.d.Sidecars, p.d.SidecarsListed = listedSidecars(mf.Spec.Path, l.Names), true
	}
	p.plan()
}

// listGateOpen is §6.4.3's gates 3 to 5, and a sixth: a block on the disk
// (an unreadable directory, a missing video) relists on its retry, or it
// could never lift.
func (p *pass) listGateOpen() bool {
	in, st := p.in, &p.in.File.Status
	last, b := in.LastListed, st.Subtitles
	switch {
	case last == nil, last.Path != in.File.Spec.Path, last.ProbeHash != st.ProbeHash:
		return true
	case b == nil:
		return true
	case b.Phase == catalogv1alpha1.SubtitlesPhaseBlocked && (b.Reason == ReasonMediaDirUnreadable || b.Reason == ReasonNotOnDisk):
		return true
	}
	return false
}

// block keeps items, attempts, nextSearchAt and dispatches verbatim (§6.4.1).
func (p *pass) block(reason, message string, wake time.Time) {
	b := p.d.Block
	if b == nil {
		b = &catalogv1alpha1.SubtitlesStatus{}
	}
	b.Phase, b.Reason, b.Message = catalogv1alpha1.SubtitlesPhaseBlocked, reason, message
	p.d.Block, p.d.Wake = b, wake
}

func (p *pass) plan() {
	in, mf := p.in, p.in.File
	st, now, spec := &mf.Status, in.Now, p.winner.Spec
	pp, profileLangs := plannerProfile(spec)
	p.extract = extractableStreams(st.MediaInfo, spec.Embedded, providerset.Order(slices.Clone(in.Extractors), spec.Providers))
	existing := buildExisting(st.MediaInfo, spec.Embedded, filepath.Base(mf.Spec.Path), sidecarNames(p.d.Sidecars), profileLangs, p.extract)
	audio := audioLanguages(st.MediaInfo, in.Item.OriginalLanguage)
	wantedKeys, cutoffMet := subtitles.Plan(pp, audio, existingKeys(existing))
	anyWant, _ := subtitles.Plan(pp, audio, nil)
	p.cad, p.threshold = cadenceFor(spec), minScoreFor(mf.Spec.MediaRef.Kind, spec)
	p.want = map[string]bool{}
	for _, k := range wantedKeys {
		p.want[string(k)] = true
	}

	b := p.d.Block
	if b == nil {
		b = &catalogv1alpha1.SubtitlesStatus{}
	}
	items, dropped := planItems(b.Items, profileKeys(pp), wantedKeys)
	for _, it := range dropped {
		if inFlight(it) {
			p.d.Withdraws = append(p.d.Withdraws, Withdraw{LangKey: it.LangKey, Seq: it.Dispatch.Seq})
		}
	}
	// New bytes: every language's history describes a file that is gone;
	// what is in flight is closed and withdrawn below unless this pass
	// dispatches it again.
	stale := map[string]int64{}
	if b.ProbeHash != "" && b.ProbeHash != st.ProbeHash {
		for i := range items {
			it := &items[i]
			it.Attempts, it.NextSearchAt = commonv1.Attempts{}, nil
			if inFlight(*it) {
				stale[it.LangKey] = it.Dispatch.Seq
				it.Dispatch.AnsweredSeq, it.Dispatch.Withdrawn = it.Dispatch.Seq, true
			}
		}
	}
	if l := in.Listing; l != nil {
		for i := range items {
			rebind(&items[i], mf.Spec.Path, string(spec.HIExtension), l.Names)
		}
	}

	p.throttled = map[string]time.Time{}
	for i := range items {
		it := &items[i]
		if _, ok := stale[it.LangKey]; !ok {
			p.incorporate(it)
		}
		if p.consider(it) {
			delete(stale, it.LangKey)
		}
		// nextSearchAt is derived after the dispatch decision, from the
		// attempts a dispatch just stamped.
		at, _, scheduled := schedule(now, *it, p.want[it.LangKey], p.cad)
		if hold := p.throttled[it.LangKey]; hold.After(at) {
			at = hold
		}
		it.NextSearchAt = &metav1.Time{Time: at}
		if scheduled && !inFlight(*it) && at.After(now) {
			p.at(at) // a past-due item waits on a gate, which set its own wake
		}
	}
	for _, k := range slices.Sorted(maps.Keys(stale)) {
		p.d.Withdraws = append(p.d.Withdraws, Withdraw{LangKey: k, Seq: stale[k]})
	}
	if p.forced {
		p.d.HandledNonce = in.ForceNonce // in the same apply as the forced dispatches (§6.5)
		if !p.forcedAny {
			p.d.Events = append(p.d.Events, Event{
				Type: "Normal", Reason: EventNothingToSearch,
				Message: fmt.Sprintf("%s=%s: no language is wanted or upgradable", catalogv1alpha1.AnnotationSubtitleSearch, in.ForceNonce),
			})
		}
	}

	searching := false
	for i := range items {
		it := &items[i]
		switch {
		case onDisk(*it):
			it.State = catalogv1alpha1.SubtitleStateDownloaded
			if upgradeCandidate(now, *it, p.cad) {
				it.State = catalogv1alpha1.SubtitleStateUpgradable
			}
		case inFlight(*it):
			it.State = catalogv1alpha1.SubtitleStateSearching
		case it.State == "", it.State == catalogv1alpha1.SubtitleStateSearching,
			it.State == catalogv1alpha1.SubtitleStateDownloaded, it.State == catalogv1alpha1.SubtitleStateUpgradable:
			it.State = catalogv1alpha1.SubtitleStatePending
		}
		if inFlight(*it) {
			searching = true
			p.at(it.Dispatch.DispatchedAt.Add(RequestTimeout))
		}
	}
	slices.SortFunc(items, func(a, b catalogv1alpha1.SubtitleItemStatus) int { return strings.Compare(a.LangKey, b.LangKey) })
	b.Items = items[:min(len(items), maxItems)]
	if len(b.Items) == 0 {
		b.Items = nil
	}
	b.Profile, b.Reason, b.Message = p.winner.Name, "", ""
	b.Wanted = stringKeys(wantedKeys)
	b.Wanted = b.Wanted[:min(len(b.Wanted), maxItems)]
	b.CutoffMet = cutoffMet
	b.ProbeHash = ""
	if len(b.Items) > 0 {
		b.ProbeHash = st.ProbeHash
	}
	switch {
	case searching:
		b.Phase = catalogv1alpha1.SubtitlesPhaseSearching
	case len(wantedKeys) > 0:
		b.Phase = catalogv1alpha1.SubtitlesPhaseWanted
	case len(anyWant) == 0 && len(b.Items) == 0:
		b.Phase = catalogv1alpha1.SubtitlesPhaseNotWanted
	default:
		b.Phase = catalogv1alpha1.SubtitlesPhaseSatisfied
	}
	p.d.Block = b
	if !p.wake.IsZero() {
		p.d.Wake = maxTime(p.wake, now.Add(minWake))
	}
}

// consider is §6.4.5-§6.4.6's dispatch decision for one item; it reports
// whether it dispatched (or adopted a dispatch).
func (p *pass) consider(it *catalogv1alpha1.SubtitleItemStatus) bool {
	in, now := p.in, p.in.Now
	if p.forced && (p.want[it.LangKey] || upgradeCandidate(now, *it, p.cad)) {
		// §6.5: every wanted language and every upgrade candidate now, in
		// flight or not, on the high lane, past the backlog gate and the
		// pacer. A dispatch in flight is superseded by the new sequence; its
		// worker finds the newer record and acks.
		e, _ := p.record(it.LangKey)
		p.dispatch(it, e, !p.want[it.LangKey], true)
		p.forcedAny = true
		return true
	}
	at, upgrade, scheduled := schedule(now, *it, p.want[it.LangKey], p.cad)
	if !scheduled || now.Before(at) || inFlight(*it) {
		return false
	}
	if !in.BacklogOpen {
		p.at(now.Add(backlogRetry)) // no record, no stamp (§6.4.6)
		return false
	}
	if in.PacerSlot.IsZero() {
		p.askReserve()
		return false
	}
	if in.PacerSlot.After(now) {
		p.at(in.PacerSlot.Add(jitter(string(in.File.UID))))
		return false
	}
	e, _ := p.record(it.LangKey)
	if r := e.Record; e.OK && r.Verdict == schema.SubtitleVerdictThrottled && r.ThrottledUntil != nil && r.ThrottledUntil.After(now) {
		p.throttled[it.LangKey] = *r.ThrottledUntil // §6.6: nextSearchAt at or after ThrottledUntil
		p.at(*r.ThrottledUntil)
		return false
	}
	p.askList() // gate 1
	if p.adopt(it, e) {
		return true
	}
	p.dispatch(it, e, upgrade, false)
	return true
}

// incorporate is §6.6's incorporation table for an item in flight.
func (p *pass) incorporate(it *catalogv1alpha1.SubtitleItemStatus) {
	d := it.Dispatch
	if d == nil || d.Seq <= d.AnsweredSeq {
		return
	}
	e, read := p.record(it.LangKey)
	if !read {
		return // a Needs run before the records are read
	}
	now, probeHash, rec := p.in.Now, p.in.File.Status.ProbeHash, e.Record
	answered := e.OK && records.TerminalAnswered(rec.State)
	timedOut := !now.Before(d.DispatchedAt.Add(RequestTimeout))
	switch {
	case answered && rec.Seq >= d.Seq:
		d.Seq, d.AnsweredSeq, d.Withdrawn = rec.Seq, rec.Seq, false
		if rec.ProbeHash == probeHash {
			p.apply(it, rec)
			p.askList() // gate 2: the worker wrote or replaced a file
		}
	case answered && rec.Seq > d.AnsweredSeq && rec.ProbeHash == probeHash:
		// An answer the item never incorporated: a newer dispatch's request
		// missed its CAS against it. Take it, keep d.Seq, owe the request
		// for d.Seq.
		p.apply(it, rec)
		p.askList()
		d.AnsweredSeq = rec.Seq
		p.owe(it, e, rec.Seq)
	case e.OK && rec.State == records.StateRequested && rec.Seq == d.Seq:
		if !timedOut {
			// §4.7; a task still stored is absorbed anyway, so it is skipped,
			// and a forced pass supersedes the dispatch instead.
			if now.Sub(d.DispatchedAt.Time) < records.RepublishWindow && !p.in.Queued[it.LangKey] && !p.forced {
				p.republish(it)
			}
			return
		}
		p.d.Withdraws = append(p.d.Withdraws, Withdraw{LangKey: it.LangKey, Seq: d.Seq})
	case e.OK && rec.State == records.StateWithdrawn && rec.Seq == d.Seq:
		d.AnsweredSeq, d.Withdrawn = d.Seq, true
		it.LastError = "request withdrawn"
		if timedOut {
			it.LastError = "no answer within 26h"
		}
	case e.OK && rec.Seq > d.Seq:
		if rec.State == records.StateRequested && p.matches(rec) {
			d.Seq, d.DispatchedAt = rec.Seq, p.requestedAt(rec) // adopted (§4.7)
			return
		}
		// A newer request with other inputs: this dispatch is closed and
		// the schedule dispatches above it.
		d.AnsweredSeq, it.LastError = d.Seq, "superseded by a newer request"
	case now.Sub(d.DispatchedAt.Time) < records.RepublishWindow:
		// Absent, withdrawn or requested below d.Seq, or an older answer
		// already incorporated: owed (§4.7). A crash or a CAS miss between
		// the apply and the record write heals here.
		p.owe(it, e, d.AnsweredSeq)
	default:
		d.AnsweredSeq, it.LastError = d.Seq, "request lost"
	}
}

// adopt is §6.4.6 step 1 for a due item: a requested record above the
// item's last dispatch with matching inputs is taken (and republished while
// inside the republish window); an answered one above it is incorporated.
// Either returns true.
func (p *pass) adopt(it *catalogv1alpha1.SubtitleItemStatus, e subtitlestore.Entry) bool {
	if !e.OK {
		return false
	}
	rec := e.Record
	var last, answered int64
	if it.Dispatch != nil {
		last, answered = it.Dispatch.Seq, it.Dispatch.AnsweredSeq
	}
	if rec.Seq <= last {
		return false
	}
	switch {
	case rec.State == records.StateRequested && p.matches(rec):
		it.Dispatch = &catalogv1alpha1.Dispatch{Seq: rec.Seq, AnsweredSeq: answered, DispatchedAt: p.requestedAt(rec)}
		if p.in.Now.Sub(rec.RequestedAt) < records.RepublishWindow && !p.in.Queued[it.LangKey] {
			p.republish(it)
		}
		return true
	case records.TerminalAnswered(rec.State):
		it.Dispatch = &catalogv1alpha1.Dispatch{Seq: rec.Seq, AnsweredSeq: rec.Seq, DispatchedAt: p.requestedAt(rec)}
		if rec.ProbeHash == p.in.File.Status.ProbeHash {
			p.apply(it, rec)
		}
		return true
	}
	return false
}

// matches reports whether rec was requested for this file's bytes under
// the winning profile's generation.
func (p *pass) matches(rec schema.SubtitleRecord) bool {
	return rec.ProbeHash == p.in.File.Status.ProbeHash && rec.ProfileGeneration == p.winner.Generation
}

// dispatch issues a new sequence for it (§6.4.6 steps 2-4).
func (p *pass) dispatch(it *catalogv1alpha1.SubtitleItemStatus, e subtitlestore.Entry, upgrade, forced bool) {
	now := p.in.Now
	seq := p.issue(e.Record.Seq)
	var answered int64
	if it.Dispatch != nil {
		answered = it.Dispatch.AnsweredSeq
	}
	it.Dispatch = &catalogv1alpha1.Dispatch{Seq: seq, AnsweredSeq: answered, DispatchedAt: metav1.NewTime(now)}
	it.Attempts = stamp(it.Attempts, now)
	prio := PriorityNormal
	switch {
	case forced:
		prio = PriorityHigh
	case upgrade:
		prio = PriorityLow
	}
	p.d.Dispatches = append(p.d.Dispatches, Dispatch{
		LangKey: it.LangKey, Seq: seq, Record: true,
		Revision: e.Revision, IncorporatedThrough: answered, New: true, Priority: prio,
		Task: p.task(it, seq, upgrade, forced),
	})
}

// owe re-requests it's dispatch at its own sequence (§4.7's owed effect).
func (p *pass) owe(it *catalogv1alpha1.SubtitleItemStatus, e subtitlestore.Entry, through int64) {
	p.d.Dispatches = append(p.d.Dispatches, Dispatch{
		LangKey: it.LangKey, Seq: it.Dispatch.Seq, Record: true, Revision: e.Revision, IncorporatedThrough: through,
		Priority: p.priorityOf(it), Task: p.task(it, it.Dispatch.Seq, p.upgradeOf(it), false),
	})
}

// republish publishes it's outstanding request again: under the same
// Msg-Id when nothing says the task is gone, else a fresh one (§6.4.6).
func (p *pass) republish(it *catalogv1alpha1.SubtitleItemStatus) {
	p.d.Dispatches = append(p.d.Dispatches, Dispatch{
		LangKey: it.LangKey, Seq: it.Dispatch.Seq, Republish: true, Evicted: p.in.Queued != nil,
		Priority: p.priorityOf(it), Task: p.task(it, it.Dispatch.Seq, p.upgradeOf(it), false),
	})
}

func (p *pass) upgradeOf(it *catalogv1alpha1.SubtitleItemStatus) bool {
	return !p.want[it.LangKey] && onDisk(*it)
}

func (p *pass) priorityOf(it *catalogv1alpha1.SubtitleItemStatus) Priority {
	if p.upgradeOf(it) {
		return PriorityLow
	}
	return PriorityNormal
}

// task is §6.7.1's FetchTask for it at seq.
func (p *pass) task(it *catalogv1alpha1.SubtitleItemStatus, seq int64, upgrade, forced bool) schema.FetchTask {
	in, mf, spec := p.in, p.in.File, p.winner.Spec
	minScore := p.threshold
	if upgrade {
		minScore = upgradeMinScore(it.Score, p.threshold)
	}
	var od *schema.OnDiskSubtitle
	if onDisk(*it) {
		minScore = max(minScore, it.Score+1)
		od = &schema.OnDiskSubtitle{Name: it.Name, Score: it.Score}
	}
	q := in.Item.Query
	q.IDs = maps.Clone(q.IDs)
	q.ReleaseTitle = ""
	if mf.Spec.ImportedFrom != nil {
		q.ReleaseTitle = mf.Spec.ImportedFrom.ReleaseTitle
	}
	if q.ReleaseTitle == "" {
		base := filepath.Base(mf.Spec.Path)
		q.ReleaseTitle = strings.TrimSuffix(base, filepath.Ext(base))
	}
	var extract []commonv1.SubtitleStream
	if mi := mf.Status.MediaInfo; mi != nil {
		for _, s := range mi.Subtitles {
			if p.extract[s.Index] {
				extract = append(extract, s)
			}
		}
	}
	return schema.FetchTask{
		MediaFile: schema.Ref{Namespace: mf.Namespace, Name: mf.Name, UID: string(mf.UID)},
		Path:      mf.Spec.Path, ProbeHash: mf.Status.ProbeHash, LangKey: it.LangKey, Seq: seq,
		Profile: p.winner.Name, ProfileGeneration: p.winner.Generation,
		MinScore: minScore, UpgradeMarginPoints: spec.Upgrade.MinDeltaPoints,
		Upgrade: upgrade, Forced: forced, OnDisk: od,
		SidecarMode: uint32(sidecarMode(in.RootFolders, mf.Spec.Path, DefaultSidecarMode)),
		Query:       q, Extract: extract,
	}
}

// apply is §6.6's second table: what an incorporated answer does to it.
func (p *pass) apply(it *catalogv1alpha1.SubtitleItemStatus, rec schema.SubtitleRecord) {
	applyVerdict(it, rec)
	if rec.Verdict == schema.SubtitleVerdictThrottled && rec.ThrottledUntil != nil && rec.ThrottledUntil.After(p.in.Now) {
		p.throttled[it.LangKey] = *rec.ThrottledUntil
	}
}

// applyVerdict sets it from an answer's facts; the state is derived
// afterwards (§6.4.5).
func applyVerdict(it *catalogv1alpha1.SubtitleItemStatus, rec schema.SubtitleRecord) {
	if rec.ScoreOutOf > 0 {
		it.ScoreOutOf = rec.ScoreOutOf
	}
	if rec.State == records.StateFailed { // the worker's last delivery (§4.8)
		fail(it, catalogv1alpha1.SubtitleStateFailed, cmp.Or(rec.LastError, rec.Failure))
		return
	}
	switch rec.Verdict {
	case schema.SubtitleVerdictDownloaded:
		at := metav1.NewTime(rec.RequestedAt.UTC())
		if rec.AnsweredAt != nil {
			at = metav1.NewTime(rec.AnsweredAt.UTC())
		}
		it.Score, it.Provider, it.SubtitleID, it.Name = rec.Score, rec.Provider, rec.SubtitleID, rec.Name
		it.DownloadedAt, it.LastError = &at, ""
	case schema.SubtitleVerdictKept:
	case schema.SubtitleVerdictUnavailable:
		fail(it, catalogv1alpha1.SubtitleStateUnavailable, rec.LastError)
	case schema.SubtitleVerdictFailed:
		fail(it, catalogv1alpha1.SubtitleStateFailed, rec.LastError)
	case schema.SubtitleVerdictThrottled:
		it.LastError = rec.LastError
		if !onDisk(*it) {
			it.State = catalogv1alpha1.SubtitleStatePending
		}
	case schema.SubtitleVerdictDropped:
		it.LastError = clip("fetch dropped: "+rec.LastError, maxLastError)
	}
}

// fail records a verdict that found nothing: with a subtitle on disk only
// the error; else the candidate is cleared.
func fail(it *catalogv1alpha1.SubtitleItemStatus, state catalogv1alpha1.SubtitleState, msg string) {
	msg = clip(msg, maxLastError)
	if onDisk(*it) {
		it.LastError = msg
		return
	}
	it.State, it.LastError = state, msg
	it.Score, it.Provider, it.SubtitleID, it.Name, it.DownloadedAt = 0, "", "", "", nil
}

// inFlight reports a dispatch outstanding: Seq > AnsweredSeq.
func inFlight(it catalogv1alpha1.SubtitleItemStatus) bool {
	return it.Dispatch.InFlight()
}

func profileKeys(pp subtitles.Profile) map[string]bool {
	out := make(map[string]bool, len(pp.Languages))
	for _, l := range pp.Languages {
		out[string(l.Key)] = true
	}
	return out
}

func stringKeys(ks []subtitles.LangKey) []string {
	if len(ks) == 0 {
		return nil
	}
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = string(k)
	}
	return out
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// jitter is a file's deterministic share of maxPacerJitter, so files paced
// to one slot do not all wake at once.
func jitter(uid string) time.Duration {
	h := fnv.New32a()
	_, _ = h.Write([]byte(uid))
	return time.Duration(h.Sum32()%uint32(maxPacerJitter/time.Millisecond)) * time.Millisecond
}

// clip cuts s to at most n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// force reads subtitle.clustarr.io/search against the handled nonce
// (§6.5, §2.9): pending when set and not yet handled; invalid when it is not
// a nonce. catalogv1alpha1.HandledNonce is what status records for a value.
func (p *pass) force() (pending, invalid bool) {
	v := p.in.ForceNonce
	if v == "" || catalogv1alpha1.HandledNonce(v) == p.d.HandledNonce {
		return false, false
	}
	return true, !catalogv1alpha1.ValidNonce(v)
}

// requestedAt is when rec was requested, or now for a record that carries
// no time (dispatchedAt is required).
func (p *pass) requestedAt(rec schema.SubtitleRecord) metav1.Time {
	if rec.RequestedAt.IsZero() {
		return metav1.NewTime(p.in.Now)
	}
	return metav1.NewTime(rec.RequestedAt.UTC())
}
