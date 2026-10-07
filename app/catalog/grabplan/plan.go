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

package grabplan

import (
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/intake"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/quality"
)

// CandidateWindow is how long a search answer stays grab input (ruling R7):
// below lifecycle.BlockQuarantine, so no answer computed before a block can
// regrab the release once its tombstone is gone.
const CandidateWindow = 30 * time.Minute

// The grab planner's Event reasons.
const (
	// ReasonAtCapacity: the owner holds EntryCap entries; a candidate is
	// refused, never an entry truncated.
	ReasonAtCapacity = "AtCapacity"
)

// Candidate is one release proposed for the owner's grab.
type Candidate struct {
	// MsgID is the intake message's Msg-Id; "" for a search answer or a
	// delayed grab come due, which settle nothing.
	MsgID   string
	Release schema.ScoredRelease
	// Source is how an engine fetches the release
	// (grabsource.ResolveSource); a zero Source refuses the candidate.
	Source    commonv1.DownloadSource
	GrabbedBy commonv1.GrabSource
	Purpose   commonv1.DownloadPurpose
	// Manual is a person's pick: exempt from TranscodedFinal and temporary
	// rejections. Override is spec.override: exempt from every permanent
	// rejection, a Blocklisted tombstone included.
	Manual   bool
	Override bool
	// NoDelay grabs at once whatever the delay profile says: a Search's
	// pick or grabBest (the Search controller always grabbed at once), and
	// a delayed grab come due.
	NoDelay bool
	// Targets are the covered Episodes or Issues; empty for a single-item
	// owner.
	Targets []schema.ItemRef
	At      time.Time

	// pending is the index of the stored pending grab this candidate came
	// from (a delayed grab come due), -1 otherwise.
	pending int
}

// Covered is one item a candidate may cover: the owner itself, or one of a
// Series' Episodes or a Comic's Issues.
type Covered struct {
	Ref            schema.ItemRef
	Season, Number int32
	Absolute       *int32
	Issue          string
	// Monitored is the item and its container monitored (P2).
	Monitored bool
	// Wanted is the item out (aired, released): an automatic grab never
	// takes a release for an item not yet available. A file at or above
	// the cutoff, a transcoded file and the queue are ItemStateRejections'
	// rules.
	Wanted  bool
	Current *decision.Current
	// DonorRejected are the donor releases a graft of this item already
	// failed with (the item's status.audio.rejectedReleases).
	DonorRejected []string
}

// View is everything Decide reads for one owner.
type View struct {
	Owner     schema.ItemRef
	Kind      commonv1.MediaKind
	Now       time.Time
	Monitored bool
	// Profile is the owner's resolved QualityProfile, ProfileRef its name;
	// a nil Profile refuses every candidate.
	Profile    *quality.Profile
	ProfileRef string
	// Delay is the DelayProfile spec governing the owner (nil: no delay).
	Delay *catalogv1alpha1.DelayProfileSpec
	// Protocols are the protocols an enabled DownloadClient serves (P7).
	Protocols           map[commonv1.Protocol]bool
	OriginalLanguageTag string
	// Entries are the stored entries: the queue and the Blocklisted
	// tombstones. Pending is pendingGrab (as a one-element list) or
	// pendingGrabs.
	Entries    []catalogv1alpha1.DownloadEntry
	Pending    []catalogv1alpha1.PendingGrab
	Covered    []Covered
	Candidates []Candidate
	// GrabLimited maps an Indexer at its grab limit to when its window next
	// has room (the ring, read without reserving: the index agent's payload
	// fetch reserves, P95).
	GrabLimited map[string]time.Time
	// Seeds are each Indexer's seed criteria.
	Seeds    map[string]*commonv1.SeedCriteria
	EntryCap int
	// PendingCap caps Pending (status.pendingGrabs' MaxItems; 1 for a
	// single-item owner).
	PendingCap int
}

// Plan is Decide's answer.
type Plan struct {
	// NewEntries are Pending entries, uid and dispatch.seq issued, for the
	// downloads stage to admit through lifecycle.
	NewEntries []catalogv1alpha1.DownloadEntry
	// Pending is the complete pending set the owner keeps.
	Pending []catalogv1alpha1.PendingGrab
	// Settle is every intake candidate's outcome, by Msg-Id.
	Settle map[string]intake.Outcome
	// Due is the soonest grabAt.
	Due    time.Time
	Events []lifecycle.Event
	// Decisions counts the plan's verdicts for metrics: "grabbed",
	// "delayed", "grabLimited", "refused".
	Decisions map[string]int
}

// verdict is one candidate's re-check.
type verdict struct {
	c      Candidate
	items  []Covered
	reason string
}

// Decide is the owner key's grab decision (ADR-0019 §6.6 steps 1-5).
func Decide(v View) Plan {
	d := &decider{v: v, now: v.Now.UTC()}
	d.plan.Settle = map[string]intake.Outcome{}
	d.plan.Decisions = map[string]int{}
	d.taken = map[string]bool{}

	// Delayed grabs come due are candidates again, from status alone
	// (P17-P18 retire); the rest are kept.
	cands := make([]Candidate, 0, len(v.Candidates)+len(v.Pending))
	for i := range v.Candidates {
		c := v.Candidates[i]
		c.pending = -1
		cands = append(cands, c)
	}
	for i := range v.Pending {
		pg := v.Pending[i]
		if pg.GrabAt.After(d.now) {
			d.pending = append(d.pending, *pg.DeepCopy())
			continue
		}
		if c, ok := fromPending(pg, v.Covered, i); ok {
			cands = append(cands, c)
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].At.After(cands[j].At) })

	var approved []verdict
	for _, c := range cands {
		vd := d.check(c)
		if vd.reason != "" {
			d.settle(c, intake.Refused, "refused")
			continue
		}
		approved = append(approved, vd)
	}
	sort.SliceStable(approved, func(i, j int) bool { return d.better(approved[i], approved[j]) })

	var limited []verdict
	for _, vd := range approved {
		// The delay first: the grab limit matters when the grab is made,
		// which for a delayed release is when its delay has passed.
		switch {
		case d.overlaps(vd):
			d.settle(vd.c, intake.Refused, "refused")
		case d.delays(vd):
			d.hold(vd, d.now.Add(d.wait(vd)), true)
		case d.limitedUntil(vd).After(d.now):
			limited = append(limited, vd)
		default:
			d.grab(vd)
		}
	}
	// Every approved release's indexer was at its grab limit: hold the one
	// whose indexer has room soonest (P13), never a donor (the next donor
	// search tries again).
	sort.SliceStable(limited, func(i, j int) bool { return d.limitedUntil(limited[i]).Before(d.limitedUntil(limited[j])) })
	for _, vd := range limited {
		if vd.c.Purpose != "" || d.overlaps(vd) {
			d.settle(vd.c, intake.Refused, "grabLimited")
			continue
		}
		d.hold(vd, d.limitedUntil(vd), false)
	}

	d.plan.Pending = d.pending
	for i := range d.pending {
		if at := d.pending[i].GrabAt.UTC(); d.plan.Due.IsZero() || at.Before(d.plan.Due) {
			d.plan.Due = at
		}
	}
	return d.plan
}

type decider struct {
	v       View
	now     time.Time
	plan    Plan
	pending []catalogv1alpha1.PendingGrab
	// taken are the purpose/item keys a grab or a hold this pass covers.
	taken     map[string]bool
	atCapOnce bool
}

func (d *decider) settle(c Candidate, o intake.Outcome, decision string) {
	d.plan.Decisions[decision]++
	if c.MsgID != "" {
		d.plan.Settle[c.MsgID] = o
	}
}

// check re-checks one candidate against the owner's fresh state.
func (d *decider) check(c Candidate) verdict {
	vd := verdict{c: c}
	rel := c.Release.Decision.ReleaseInfo
	switch {
	case d.v.Profile == nil:
		vd.reason = "the owner has no usable quality profile"
	case c.MsgID == "" && c.pending < 0 && d.now.Sub(c.At) > CandidateWindow:
		vd.reason = "the search answer is older than the candidate window"
	case len(rel.GUID) > maxGUID:
		vd.reason = "the release GUID is too long to keep"
	case sourceEmpty(c.Source):
		vd.reason = "the release has nothing to download it by"
	case !d.v.Monitored && !c.Manual:
		vd.reason = "the owner is not monitored"
	case !d.protocolEnabled(rel.Protocol):
		vd.reason = "no enabled DownloadClient or delay profile takes " + string(rel.Protocol)
	case d.hasEntry(lifecycle.EntryID(d.v.Owner.Name, c.Purpose, rel.GUID)):
		vd.reason = "the release is already a grab of the owner"
	}
	if vd.reason != "" {
		return vd
	}
	if ok, why := releaseOK(c); !ok {
		vd.reason = why
		return vd
	}
	for _, it := range d.coverage(c) {
		if d.itemOK(c, it) {
			vd.items = append(vd.items, it)
		}
	}
	if len(vd.items) == 0 {
		// P23: a pack covering no wanted episode is refused; one covering
		// some is taken as covering those.
		vd.reason = "no item it covers wants it"
	}
	return vd
}

// itemOK re-checks the candidate for one covered item: monitored and out
// (unless a person picked it), and no item-state rejection it is not exempt
// from.
func (d *decider) itemOK(c Candidate, it Covered) bool {
	if !c.Manual && (!it.Monitored || !it.Wanted) {
		return false
	}
	rel := c.Release.Decision.ReleaseInfo
	t := decision.Target{
		Kind:                commonv1.MediaKind(strings.ToLower(it.Ref.Kind)),
		OriginalLanguageTag: d.v.OriginalLanguageTag,
		Current:             it.Current,
		Queue:               d.queue(it, c.Purpose),
	}
	blocked := c.Release.Blocked != nil || d.tombstoned(it, rel)
	t.Blocklist = func(string, string) bool { return blocked }
	if c.Purpose != "" {
		t.Donor = &decision.Donor{Rejected: it.DonorRejected}
	}
	dec := decision.Decision{Release: rel, Score: int(c.Release.Score)}
	for _, rej := range decision.ItemStateRejections(ctxBackground, t, *d.v.Profile, dec, decision.Options{}) {
		if !exempt(c, rej) {
			return false
		}
	}
	return true
}

// releaseOK is the agent's verdict, with a person's exemptions: an approved
// release; a pick with only temporary rejections or TranscodedFinal; any
// release with spec.override.
func releaseOK(c Candidate) (bool, string) {
	d := c.Release.Decision
	if d.Approved {
		return true, ""
	}
	if !c.Manual && !c.Override {
		return false, "the release was not approved"
	}
	for _, rej := range d.Rejections {
		if !exempt(c, rej) {
			return false, "the release was rejected: " + rej.Reason
		}
	}
	return true, ""
}

// exempt reports a rejection a person's pick passes (resolveGrab's rule).
func exempt(c Candidate, rej commonv1.Rejection) bool {
	if c.Override && rej.Type == commonv1.RejectionPermanent {
		return true
	}
	return c.Manual && (decision.ReasonTranscodedFinal.Of(rej) || rej.Type == commonv1.RejectionTemporary)
}

// better orders approved candidates best first (decision.Rank's order, P13,
// over what a scored release carries): a person's pick, then quality tier,
// revision (unless the profile does not prefer it), format score, how many
// items it covers, indexer priority, the agent's own rank, and the newest.
func (d *decider) better(a, b verdict) bool {
	if a.c.Manual != b.c.Manual {
		return a.c.Manual
	}
	ra, rb := a.c.Release.Decision.ReleaseInfo, b.c.Release.Decision.ReleaseInfo
	if a.c.Purpose == "" && b.c.Purpose == "" {
		ia, ib := tier(d.v.Profile, ra.Quality), tier(d.v.Profile, rb.Quality)
		if ia != ib {
			return ia < ib
		}
		if d.v.Profile.ProperPolicy != "doNotPrefer" {
			if c := compareRevision(ra.Revision, rb.Revision); c != 0 {
				return c > 0
			}
		}
		if a.c.Release.Score != b.c.Release.Score {
			return a.c.Release.Score > b.c.Release.Score
		}
	}
	if len(a.items) != len(b.items) {
		return len(a.items) > len(b.items)
	}
	if pa, pb := priority(a.c.Release.IndexerPriority), priority(b.c.Release.IndexerPriority); pa != pb {
		return pa < pb
	}
	if ka, kb := rank(a.c.Release.Decision.Rank), rank(b.c.Release.Decision.Rank); ka != kb {
		return ka < kb
	}
	return a.c.At.After(b.c.At)
}

func tier(p *quality.Profile, q commonv1.Quality) int {
	if i, ok := p.Index(q); ok {
		return i
	}
	return 1 << 20
}

func priority(p int32) int32 {
	if p == 0 {
		return 25
	}
	return p
}

func rank(r int32) int32 {
	if r <= 0 {
		return 1 << 30
	}
	return r
}

func compareRevision(x, y commonv1.Revision) int {
	if x.Real != y.Real {
		if x.Real > y.Real {
			return 1
		}
		return -1
	}
	if xv, yv := x.EffectiveVersion(), y.EffectiveVersion(); xv != yv {
		if xv > yv {
			return 1
		}
		return -1
	}
	return 0
}

// coverKey is one item under one purpose.
func coverKey(purpose commonv1.DownloadPurpose, it Covered) string {
	return string(purpose) + "/" + it.Ref.Name
}

// overlaps reports a candidate covering an item a grab or hold this pass
// already covers.
func (d *decider) overlaps(vd verdict) bool {
	for _, it := range vd.items {
		if d.taken[coverKey(vd.c.Purpose, it)] {
			return true
		}
	}
	return false
}

func (d *decider) take(vd verdict) {
	for _, it := range vd.items {
		d.taken[coverKey(vd.c.Purpose, it)] = true
	}
}

// limitedUntil is when the candidate's indexer next has a grab slot, zero
// when it has one now.
func (d *decider) limitedUntil(vd verdict) time.Time {
	ref := vd.c.Release.Decision.IndexerRef
	if ref == "" {
		return time.Time{}
	}
	return d.v.GrabLimited[ref]
}

// delays reports a candidate the delay profile holds (P15): never a donor,
// a person's pick or a Search's grab, nor one the profile bypasses.
func (d *decider) delays(vd verdict) bool {
	if vd.c.NoDelay || vd.c.Manual || vd.c.Purpose != "" || d.v.Delay == nil {
		return false
	}
	wait := d.wait(vd)
	if wait <= 0 {
		return false
	}
	rel := vd.c.Release.Decision.ReleaseInfo
	idx, ok := d.v.Profile.Index(rel.Quality)
	return !bypasses(*d.v.Delay, ok && idx == 0, vd.c.Release.Score)
}

func (d *decider) wait(vd verdict) time.Duration {
	if d.v.Delay == nil {
		return 0
	}
	return delayFor(*d.v.Delay, vd.c.Release.Decision.Protocol)
}

// grab makes the candidate a new Pending entry, or refuses it at the
// owner's cap.
func (d *decider) grab(vd verdict) {
	if d.v.EntryCap > 0 && len(d.v.Entries)+len(d.plan.NewEntries) >= d.v.EntryCap {
		d.settle(vd.c, intake.Refused, "refused")
		if !d.atCapOnce {
			d.atCapOnce = true
			d.plan.Events = append(d.plan.Events, lifecycle.Event{
				Type: lifecycle.EventNormal, Reason: ReasonAtCapacity,
				Message: "the owner holds its maximum of grab entries; a release was not grabbed",
			})
		}
		return
	}
	e := d.entry(vd)
	d.plan.NewEntries = append(d.plan.NewEntries, e)
	d.take(vd)
	d.dropPending(vd)
	d.settle(vd.c, intake.Incorporated, "grabbed")
}

// hold keeps the candidate as a pending grab until at (P16): it replaces an
// overlapping pending grab only when it is an upgrade of it, keeping the
// window's anchor for a delay (keepAnchor), or the later instant for a
// grab-limit hold.
func (d *decider) hold(vd verdict, at time.Time, keepAnchor bool) {
	pg := d.pendingGrab(vd, at)
	for i := range d.pending {
		cur := &d.pending[i]
		if !d.pendingOverlaps(cur, vd) {
			continue
		}
		if cur.Candidate != nil && d.v.Profile.UpgradeDecision(pendingQuality(cur), candidateQuality(vd.c)) != quality.Upgrade {
			d.settle(vd.c, intake.Refused, "refused")
			return
		}
		switch {
		case keepAnchor:
			pg.GrabAt = cur.GrabAt
		case cur.GrabAt.After(at):
			pg.GrabAt = cur.GrabAt
		}
		*cur = pg
		d.take(vd)
		d.settle(vd.c, intake.Pended, decisionOf(keepAnchor))
		return
	}
	if d.v.PendingCap > 0 && len(d.pending) >= d.v.PendingCap {
		d.settle(vd.c, intake.Refused, "refused")
		return
	}
	d.pending = append(d.pending, pg)
	d.take(vd)
	d.settle(vd.c, intake.Pended, decisionOf(keepAnchor))
}

func decisionOf(delay bool) string {
	if delay {
		return "delayed"
	}
	return "grabLimited"
}

// dropPending removes the stored pending grabs a grab now covers: their
// candidate is consumed or superseded.
func (d *decider) dropPending(vd verdict) {
	kept := d.pending[:0]
	for i := range d.pending {
		if !d.pendingOverlaps(&d.pending[i], vd) {
			kept = append(kept, d.pending[i])
		}
	}
	d.pending = kept
}

// pendingOverlaps reports a pending grab covering an item vd covers: on a
// single-item owner every pending grab does.
func (d *decider) pendingOverlaps(pg *catalogv1alpha1.PendingGrab, vd verdict) bool {
	if vd.c.Purpose != "" {
		return false // a donor is never held, and holds nothing
	}
	if len(pg.Episodes) == 0 && len(pg.Issues) == 0 {
		return true
	}
	for _, it := range vd.items {
		for _, n := range pg.Episodes {
			if n.Season == it.Season && n.Number == it.Number && it.Issue == "" {
				return true
			}
		}
		for _, iss := range pg.Issues {
			if iss == it.Issue && iss != "" {
				return true
			}
		}
	}
	return false
}

func pendingQuality(pg *catalogv1alpha1.PendingGrab) quality.Candidate {
	r := pg.Candidate.Release
	return quality.Candidate{Quality: r.Quality, Revision: r.Revision, FormatScore: int(r.FormatScore)}
}

func candidateQuality(c Candidate) quality.Candidate {
	r := c.Release.Decision.ReleaseInfo
	return quality.Candidate{Quality: r.Quality, Revision: r.Revision, FormatScore: int(c.Release.Score)}
}

// hasEntry reports a stored entry of that id.
func (d *decider) hasEntry(id string) bool {
	for i := range d.v.Entries {
		if d.v.Entries[i].ID == id {
			return true
		}
	}
	return false
}

// protocolEnabled: an enabled DownloadClient serves the protocol and the
// delay profile allows it.
func (d *decider) protocolEnabled(p commonv1.Protocol) bool {
	if !d.v.Protocols[p] {
		return false
	}
	return d.v.Delay == nil || protocolEnabled(*d.v.Delay, p)
}

// queue is the live entries covering it, of purpose, as pkg/decision's
// queue.
func (d *decider) queue(it Covered, purpose commonv1.DownloadPurpose) []decision.Queued {
	var out []decision.Queued
	for i := range d.v.Entries {
		e := &d.v.Entries[i]
		if !live(e) || e.Purpose != purpose || !covers(e, it) {
			continue
		}
		out = append(out, quality.Candidate{Quality: e.Release.Quality, Revision: e.Release.Revision, FormatScore: int(e.Release.FormatScore)})
	}
	return out
}

// tombstoned reports a Blocklisted entry covering it for the same release.
func (d *decider) tombstoned(it Covered, rel commonv1.ReleaseInfo) bool {
	for i := range d.v.Entries {
		e := &d.v.Entries[i]
		if e.Phase != commonv1.DownloadPhaseBlocklisted || !covers(e, it) {
			continue
		}
		if sameRelease(e.Release, rel) {
			return true
		}
	}
	return false
}

func sameRelease(r catalogv1alpha1.DownloadRelease, rel commonv1.ReleaseInfo) bool {
	if rel.InfoHash != "" && strings.EqualFold(r.InfoHash, rel.InfoHash) {
		return true
	}
	return rel.GUID != "" && r.GUID == rel.GUID && r.IndexerRef == rel.IndexerRef
}

// live reports an entry before Removing that is not terminal: lifecycle's
// rule, restated (rollup links controller-runtime).
func live(e *catalogv1alpha1.DownloadEntry) bool {
	switch e.Phase {
	case commonv1.DownloadPhaseImported, commonv1.DownloadPhaseRemoving, commonv1.DownloadPhaseFailed, commonv1.DownloadPhaseBlocklisted:
		return false
	}
	return true
}

// entry is a new Pending entry for vd (§6.6 step 2).
func (d *decider) entry(vd verdict) catalogv1alpha1.DownloadEntry {
	c := vd.c
	rel := c.Release.Decision.ReleaseInfo
	now := metav1.NewTime(d.now)
	e := catalogv1alpha1.DownloadEntry{
		ID:                 lifecycle.EntryID(d.v.Owner.Name, c.Purpose, rel.GUID),
		UID:                newUID(),
		Purpose:            c.Purpose,
		Release:            DownloadRelease(rel, c.Release.Score),
		Source:             *c.Source.DeepCopy(),
		GrabbedBy:          c.GrabbedBy,
		GrabbedAt:          now,
		Manual:             c.Manual,
		QualityProfileRef:  clamp(d.v.ProfileRef, 253),
		RemoveOnImport:     true,
		RemoveDataOnDelete: true,
		Phase:              commonv1.DownloadPhasePending,
		Dispatch:           catalogv1alpha1.Dispatch{Seq: nextSeq(d.now), DispatchedAt: now},
	}
	if s := d.v.Seeds[rel.IndexerRef]; s != nil {
		e.SeedCriteria = s.DeepCopy()
	}
	if container(d.v.Kind) {
		e.Episodes, e.Issues = numbers(vd.items)
	}
	if e.GrabbedBy == "" {
		e.GrabbedBy = commonv1.GrabSourceSearch
	}
	return e
}

// pendingGrab is vd held until at.
func (d *decider) pendingGrab(vd verdict, at time.Time) catalogv1alpha1.PendingGrab {
	c := vd.c
	rel := c.Release.Decision.ReleaseInfo
	dr := DownloadRelease(rel, c.Release.Score)
	pg := catalogv1alpha1.PendingGrab{
		ReleaseTitle: dr.Title,
		Protocol:     rel.Protocol,
		GrabAt:       metav1.NewTime(at.UTC().Truncate(time.Second)),
		Candidate: &catalogv1alpha1.GrabCandidate{
			Release: dr, Source: *c.Source.DeepCopy(), Score: c.Release.Score,
			GrabbedBy: c.GrabbedBy, Purpose: c.Purpose, Manual: c.Manual,
		},
	}
	if container(d.v.Kind) {
		pg.Episodes, pg.Issues = numbers(vd.items)
	}
	return pg
}

// nextSeq is records.NextSeq(0, 0, now): a new entry's first seq.
func nextSeq(now time.Time) int64 { return max(1, now.UnixMilli()) }
