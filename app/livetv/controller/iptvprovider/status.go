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

package iptvprovider

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"

	clustarrac "github.com/mediactl/clustarr/api/applyconfiguration/clustarr/v1alpha1"
	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/tuner"
	"github.com/mediactl/clustarr/pkg/iptv/plexguide"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// writeStatus renders the provider's whole status, on every path, from the
// tuner's snapshot -- or, when the tuner does not answer for it, from what
// the status already says, so an early return never releases a field
// (CLAUDE.md's SSA gotchas).
func (r *Reconciler) writeStatus(ctx context.Context, p *clustarrv1.IPTVProvider, pl plan, valid metav1.Condition, res resolved, disabled bool, now time.Time) error {
	_, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerLiveTV, render(p, r.Address, pl, r.Tuner, valid, res, disabled, now))
	return err
}

// render is the provider's whole status under k8s.ManagerLiveTV. res says
// why a guide source has no token; now is the clock a rate limit's hold is
// read against.
func render(p *clustarrv1.IPTVProvider, address string, pl plan, tn Tuner, valid metav1.Condition, res resolved, disabled bool, now time.Time) *clustarrac.IPTVProviderApplyConfiguration {
	key := types.NamespacedName{Namespace: p.Namespace, Name: p.Name}
	snap, ok := tn.Snapshot(key)
	if disabled {
		ok = false
	}
	if address == "" {
		address = p.Status.Address
	}
	st := clustarrac.IPTVProviderStatus().WithObservedGeneration(p.Generation)
	if address != "" {
		st.WithAddress(address)
	}
	guideSourcesOn := p.Spec.GuideSources != nil && p.Spec.GuideSources.Plex != nil
	if ok {
		fromSnapshot(st, snap)
		if p.Spec.PlexGuide != nil {
			st.WithPlexGuide(plexGuideStatus(snap))
		}
		if guideSourcesOn {
			st.WithGuideSources(guideSourcesStatus(snap.GuideSources))
		}
	} else {
		reassertKnownStatus(st, p)
	}
	published := withBlocks(st, p, pl, snap, ok)
	withCategories(st, p, pl, published)

	conds := slices.Clone(p.Status.Conditions)
	set := func(c metav1.Condition) { c.ObservedGeneration = p.Generation; meta.SetStatusCondition(&conds, c) }
	set(valid)
	playlist, guide := fetchConditions(p, snap, ok)
	set(playlist)
	set(guide)
	ready := metav1.Condition{Type: clustarrv1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", Message: "the tuner serves this provider"}
	switch {
	case disabled:
		ready = metav1.Condition{Type: clustarrv1.ConditionReady, Status: metav1.ConditionFalse, Reason: clustarrv1.ReasonDisabled, Message: "spec.enabled is false"}
	case valid.Status != metav1.ConditionTrue:
		ready = metav1.Condition{Type: clustarrv1.ConditionReady, Status: metav1.ConditionFalse, Reason: valid.Reason, Message: valid.Message}
	case pl.reason != "":
		ready = metav1.Condition{Type: clustarrv1.ConditionReady, Status: metav1.ConditionFalse, Reason: pl.reason, Message: pl.message + "; the last good blocks are kept"}
	case playlist.Status != metav1.ConditionTrue:
		ready = metav1.Condition{Type: clustarrv1.ConditionReady, Status: metav1.ConditionFalse, Reason: "PlaylistNotFetched", Message: playlist.Message}
	case guide.Status != metav1.ConditionTrue:
		ready = metav1.Condition{Type: clustarrv1.ConditionReady, Status: metav1.ConditionFalse, Reason: "GuidesNotFetched", Message: guide.Message}
	}
	set(ready)
	if p.Spec.PlexGuide != nil {
		set(plexGuideCondition(p, snap, ok))
	}
	if p.Spec.Lineup != nil {
		set(lineupCondition(p, pl))
	}
	if guideSourcesOn {
		set(guideSourcesCondition(p, snap, ok, res, now))
	}
	for _, c := range conds {
		if !ownedCondition(p, c.Type) {
			continue
		}
		st.WithConditions(metav1ac.Condition().WithType(c.Type).WithStatus(c.Status).WithReason(c.Reason).
			WithMessage(c.Message).WithLastTransitionTime(c.LastTransitionTime).WithObservedGeneration(c.ObservedGeneration))
	}
	return clustarrac.IPTVProvider(p.Name, p.Namespace).WithStatus(st)
}

// ownedCondition reports whether this manager sends a condition of type t:
// the four every provider has, and each of the others while the spec block
// it reports on is set, so it is released with that block.
func ownedCondition(p *clustarrv1.IPTVProvider, t string) bool {
	switch t {
	case clustarrv1.ConditionReady, clustarrv1.ConditionValid, clustarrv1.ConditionPlaylistFetched, clustarrv1.ConditionGuidesFetched:
		return true
	case clustarrv1.ConditionPlexGuideReady:
		return p.Spec.PlexGuide != nil
	case clustarrv1.ConditionLineupApplied:
		return p.Spec.Lineup != nil
	case clustarrv1.ConditionGuideSourcesReady:
		return p.Spec.GuideSources != nil && p.Spec.GuideSources.Plex != nil
	}
	return false
}

// withBlocks writes the planned blocks, each with its lineup and guide hashes
// from the snapshot, or, when the tuner does not answer, from the status.
//
// A planned block whose device the tuner does not serve yet keeps the
// status as it is: the status names only devices Plex can reach. The
// controller applies a new plan to the tuner and writes the status in the
// same reconcile, while the tuner serves the plan's devices only once its
// render ends -- 85 s for the owner's 257 channels on 2026-11-04, when
// argon-tv moved onto Plex's own guide: cluster-plex read the new device's
// URI from the status at once, PMS could not fetch it ("POST
// /media/grabbers/devices: plex: not found"), and the old XMLTV DVR was
// deleted before the new device registered. The tuner's Notify wakes the
// controller when the render lands, and that reconcile publishes the plan.
// It returns the blocks it wrote.
func withBlocks(st *clustarrac.IPTVProviderStatusApplyConfiguration, p *clustarrv1.IPTVProvider, pl plan, snap tuner.Snapshot, ok bool) []clustarrv1.BlockStatus {
	rendered := map[string]tuner.BlockState{}
	served := map[string]bool{}
	if ok {
		for _, b := range snap.Blocks {
			rendered[b.Start] = b
			served[b.DeviceID] = true
		}
	}
	// By device: a Plex block may start where an XMLTV block did, and
	// carries none of its hashes.
	known := map[string]clustarrv1.BlockStatus{}
	for _, b := range p.Status.Blocks {
		known[b.DeviceID] = b
	}
	blocks := pl.blocks
	if ok && snap.PlaylistFetched && !allServed(pl.blocks, served) {
		blocks = p.Status.Blocks
	}
	for _, b := range blocks {
		ac := clustarrac.BlockStatus().WithStart(b.Start).WithChannels(b.Channels).WithFirst(b.First).WithLast(b.Last).
			WithMapBytes(b.MapBytes).WithDeviceID(b.DeviceID).WithFriendlyName(b.FriendlyName)
		if b.Path != "" {
			ac.WithPath(b.Path)
		}
		if b.URI != "" {
			ac.WithURI(b.URI)
		}
		if b.GuidePath != "" {
			ac.WithGuidePath(b.GuidePath)
		}
		if b.Lineup != "" {
			ac.WithLineup(b.Lineup)
		}
		if b.Category != "" {
			ac.WithCategory(b.Category)
		}
		if b.Owner != "" {
			ac.WithOwner(b.Owner)
		}
		// A tuner that has not rendered a playlist yet (a manager just
		// started, before its cache loads) has no hashes: the status keeps
		// the last ones, or cluster-plex would save every block's map and
		// reload its guide again.
		lineupHash, guideHash := known[b.DeviceID].LineupHash, known[b.DeviceID].GuideHash
		if rb, has := rendered[b.Start]; has && snap.PlaylistFetched {
			lineupHash, guideHash = rb.LineupHash, rb.GuideHash
		}
		if lineupHash != "" {
			ac.WithLineupHash(lineupHash)
		}
		if guideHash != "" {
			ac.WithGuideHash(guideHash)
		}
		st.WithBlocks(ac)
	}
	return blocks
}

// withCategories writes status.categories while spec.lineup is set: the
// stored plan's categories in its order, each with its base, its active
// channels (counted from their status.lineup.category; a category the plan
// refuses as TooMany counts the channels the lineup controller left in it),
// and the device of its published block (a refused category's held one). A plan holding only an error (its
// rules did not compile) re-sends the categories the status has, as the
// blocks are held.
func withCategories(st *clustarrac.IPTVProviderStatusApplyConfiguration, p *clustarrv1.IPTVProvider, pl plan, published []clustarrv1.BlockStatus) {
	lp := pl.lineup
	if p.Spec.Lineup == nil || lp == nil {
		return
	}
	if lp.Error != "" && len(lp.Categories) == 0 {
		for _, c := range p.Status.Categories {
			ac := clustarrac.CategoryStatus().WithName(c.Name).WithBase(c.Base).WithChannels(c.Channels)
			if c.DeviceID != "" {
				ac.WithDeviceID(c.DeviceID)
			}
			if c.Reason != "" {
				ac.WithReason(c.Reason)
			}
			st.WithCategories(ac)
		}
		return
	}
	devices := map[string]string{}
	for _, b := range published {
		if b.Category != "" {
			devices[b.Category] = b.DeviceID
		}
	}
	seen := map[string]bool{}
	for _, c := range lp.Categories {
		if seen[c.Name] || len(seen) == maxCategories {
			continue // the list is a map by name, capped
		}
		seen[c.Name] = true
		ac := clustarrac.CategoryStatus().WithName(c.Name).WithBase(int32(c.Base)).WithChannels(pl.categoryChannels[c.Name])
		if d := devices[c.Name]; d != "" {
			ac.WithDeviceID(d)
		}
		if c.TooMany {
			ac.WithReason(clustarrv1.ReasonTooMany)
		}
		st.WithCategories(ac)
	}
}

// maxCategories is status.categories' MaxItems.
const maxCategories = 64

// lineupCondition is LineupApplied, from the stored plan: Pending before
// one is stored, InvalidLineup with its error, DryRun while spec.lineup is
// not enabled or the plan stored is a dry run's, TooMany while the plan
// refuses a category (naming it: its channels are left as they were and
// its published block is kept), else Applied.
func lineupCondition(p *clustarrv1.IPTVProvider, pl plan) metav1.Condition {
	c := metav1.Condition{Type: clustarrv1.ConditionLineupApplied, Status: metav1.ConditionFalse}
	lp := pl.lineup
	var tooMany []string
	var channels, categories int
	if lp != nil {
		for _, cat := range lp.Categories {
			if cat.TooMany {
				tooMany = append(tooMany, fmt.Sprintf("%s (%d channels)", cat.Name, cat.Channels))
				continue
			}
			categories++
			channels += cat.Channels
		}
	}
	refused := ""
	if len(tooMany) > 0 {
		refused = fmt.Sprintf("; over dvr.maxChannels (%d) or a DVR's channel-map budget, its channels left as they were and its published block kept: %s",
			p.Spec.MaxChannelsOrDefault(), strings.Join(tooMany, ", "))
	}
	// The blocks hold until the lineup controller has applied the plan to
	// every channel (Ready=False LineupPending).
	pending := ""
	if pl.reason == clustarrv1.ReasonLineupPending {
		pending = "; not on the channels yet, the published blocks are kept"
	}
	switch {
	case lp == nil:
		c.Status, c.Reason, c.Message = metav1.ConditionUnknown, "Pending", "the lineup has not been planned yet"
	case lp.Error != "":
		c.Reason, c.Message = clustarrv1.ReasonInvalidLineup, lp.Error
	case !p.Spec.Lineup.Enabled:
		c.Reason = clustarrv1.ReasonDryRun
		c.Message = "spec.lineup.enabled is false: the plan is shown in the channels' status, and no channel or block follows it" + refused
	case lp.DryRun:
		c.Reason, c.Message = clustarrv1.ReasonDryRun, "the lineup controller has not applied the plan yet"+refused
	case len(tooMany) > 0:
		c.Reason = clustarrv1.ReasonTooMany
		c.Message = fmt.Sprintf("%d channels in %d categories", channels, categories) + pending + refused
	default:
		c.Status, c.Reason = metav1.ConditionTrue, clustarrv1.ReasonApplied
		c.Message = fmt.Sprintf("%d channels in %d categories", channels, categories) + pending
	}
	return c
}

// guideSourcesStatus is the grid fetch loop's state as
// status.guideSources.plex. Its counts are no omitempty fields: always sent.
func guideSourcesStatus(s tuner.GuideSourcesState) *clustarrac.GuideSourcesStatusApplyConfiguration {
	ps := clustarrac.PlexSourceStatus().WithLineups(int32(s.Lineups)).WithStations(int32(s.Stations)).WithGrids(int32(s.Grids))
	if !s.FetchedAt.IsZero() {
		ps.WithFetchedAt(metav1.NewTime(s.FetchedAt))
	}
	if !s.HoldUntil.IsZero() {
		ps.WithHoldUntil(metav1.NewTime(s.HoldUntil))
	}
	if s.Error != "" {
		ps.WithError(k8s.ClampText(s.Error, maxSourceError))
	}
	return clustarrac.GuideSourcesStatus().WithPlex(ps)
}

// maxSourceError is status.guideSources.plex.error's MaxLength.
const maxSourceError = 1024

// guideSourcesCondition is GuideSourcesReady: the token's Secret first, then
// the grid loop's rate-limit hold, its last failure, and its first pass;
// carried over from the status when the tuner does not answer.
func guideSourcesCondition(p *clustarrv1.IPTVProvider, s tuner.Snapshot, ok bool, res resolved, now time.Time) metav1.Condition {
	c := metav1.Condition{Type: clustarrv1.ConditionGuideSourcesReady, Status: metav1.ConditionFalse}
	g := s.GuideSources
	switch {
	case res.guideTokenReason != "":
		c.Reason, c.Message = res.guideTokenReason, res.guideTokenError
	case !ok:
		if cur := meta.FindStatusCondition(p.Status.Conditions, clustarrv1.ConditionGuideSourcesReady); cur != nil {
			return *cur
		}
		c.Status, c.Reason, c.Message = metav1.ConditionUnknown, "Pending", "the tuner has not answered yet"
	case g.HoldUntil.After(now):
		c.Reason = reasonRateLimited
		c.Message = "Plex's guide service limited the requests; the grids are fetched again after " + g.HoldUntil.UTC().Format(time.RFC3339)
	case g.Error != "":
		c.Reason, c.Message = reasonFetchFailed, g.Error
	case g.FetchedAt.IsZero():
		c.Reason, c.Message = "Pending", "the grids have not been fetched yet"
	default:
		c.Status, c.Reason = metav1.ConditionTrue, "Fetched"
		c.Message = fmt.Sprintf("%d lineups, %d stations, %d grids", g.Lineups, g.Stations, g.Grids)
	}
	return c
}

// GuideSourcesReady's reasons beside the Secret's.
const (
	reasonRateLimited = "RateLimited"
	reasonFetchFailed = "FetchFailed"
)

// allServed is whether the tuner's render serves every planned block's
// device. A block without a device (a span no device was found for) needs
// none.
func allServed(blocks []clustarrv1.BlockStatus, served map[string]bool) bool {
	for _, b := range blocks {
		if b.DeviceID != "" && !served[b.DeviceID] {
			return false
		}
	}
	return true
}

// fetchConditions are PlaylistFetched and GuidesFetched: from the snapshot,
// else carried over from the status.
func fetchConditions(p *clustarrv1.IPTVProvider, snap tuner.Snapshot, ok bool) (playlist, guide metav1.Condition) {
	carry := func(t, reason, msg string) metav1.Condition {
		if c := meta.FindStatusCondition(p.Status.Conditions, t); c != nil {
			return *c
		}
		return metav1.Condition{Type: t, Status: metav1.ConditionUnknown, Reason: reason, Message: msg}
	}
	if !ok {
		return carry(clustarrv1.ConditionPlaylistFetched, "Pending", "the tuner has not reported yet"),
			carry(clustarrv1.ConditionGuidesFetched, "Pending", "the tuner has not reported yet")
	}
	playlist = metav1.Condition{
		Type: clustarrv1.ConditionPlaylistFetched, Status: metav1.ConditionTrue, Reason: "Fetched",
		Message: fmt.Sprintf("%d entries, %d candidates", snap.Playlist.Entries, snap.Playlist.Candidates),
	}
	switch {
	case !snap.PlaylistFetched && snap.Playlist.Error != "":
		playlist = metav1.Condition{Type: clustarrv1.ConditionPlaylistFetched, Status: metav1.ConditionFalse, Reason: "FetchFailed", Message: snap.Playlist.Error}
	case !snap.PlaylistFetched:
		playlist = metav1.Condition{Type: clustarrv1.ConditionPlaylistFetched, Status: metav1.ConditionFalse, Reason: "Pending", Message: "the playlist has not been fetched yet"}
	case snap.Playlist.Error != "":
		playlist.Reason, playlist.Message = "StaleAfterError", "serving the last good playlist: "+snap.Playlist.Error
	}
	if p.Spec.TypeOrDefault() == clustarrv1.ProviderTypeM3U && p.Spec.GuideURL == "" {
		return playlist, metav1.Condition{
			Type: clustarrv1.ConditionGuidesFetched, Status: metav1.ConditionTrue, Reason: "NoGuide",
			Message: "the M3U provider names no guideURL: every channel has a dummy guide",
		}
	}
	g := snap.Guide
	guide = metav1.Condition{
		Type: clustarrv1.ConditionGuidesFetched, Status: metav1.ConditionTrue, Reason: "Fetched",
		Message: fmt.Sprintf("%d channels, %d programmes", g.Channels, g.Programmes),
	}
	switch {
	case g.FetchedAt.IsZero() && g.Error != "":
		guide = metav1.Condition{Type: clustarrv1.ConditionGuidesFetched, Status: metav1.ConditionFalse, Reason: "FetchFailed", Message: g.Error}
	case g.FetchedAt.IsZero():
		guide = metav1.Condition{Type: clustarrv1.ConditionGuidesFetched, Status: metav1.ConditionFalse, Reason: "Pending", Message: "the guide has not been fetched yet"}
	case g.Error != "":
		guide.Reason, guide.Message = "StaleAfterError", "serving the last good guide: "+g.Error
	}
	return playlist, guide
}

func fromSnapshot(st *clustarrac.IPTVProviderStatusApplyConfiguration, s tuner.Snapshot) {
	pl := clustarrac.PlaylistStatus().WithEntries(int32(s.Playlist.Entries)).WithGroups(int32(s.Playlist.Groups)).
		WithCandidates(int32(s.Playlist.Candidates))
	if !s.Playlist.FetchedAt.IsZero() {
		pl.WithFetchedAt(metav1.NewTime(s.Playlist.FetchedAt))
	}
	if s.Playlist.Hash != "" {
		pl.WithHash(s.Playlist.Hash)
	}
	if s.Playlist.Error != "" {
		pl.WithError(s.Playlist.Error)
	}
	st.WithPlaylist(pl)

	g := clustarrac.GuideStatus().WithChannels(int32(s.Guide.Channels)).WithProgrammes(int32(s.Guide.Programmes))
	if !s.Guide.FetchedAt.IsZero() {
		g.WithFetchedAt(metav1.NewTime(s.Guide.FetchedAt))
	}
	if s.Guide.Error != "" {
		g.WithError(s.Guide.Error)
	}
	st.WithGuide(g)

	a := s.Account
	if !a.FetchedAt.IsZero() || a.Error != "" {
		ac := clustarrac.AccountStatus().WithMaxConnections(int32(a.MaxConnections)).WithActiveConnections(int32(a.Active))
		if a.Status != "" {
			ac.WithStatus(a.Status)
		}
		if !a.ExpiresAt.IsZero() {
			ac.WithExpiresAt(metav1.NewTime(a.ExpiresAt))
		}
		if !a.FetchedAt.IsZero() {
			ac.WithFetchedAt(metav1.NewTime(a.FetchedAt))
		}
		if a.Error != "" {
			ac.WithError(a.Error)
		}
		st.WithAccount(ac)
	}

	lu := clustarrac.LineupStatus().WithActive(int32(s.Lineup.Active)).WithUnmapped(int32(s.Lineup.Unmapped)).WithMissing(int32(s.Lineup.Missing))
	if s.Lineup.First != "" {
		lu.WithFirst(s.Lineup.First).WithLast(s.Lineup.Last)
	}
	st.WithLineup(lu)
	st.WithTuners(clustarrac.TunerStatus().WithTotal(int32(s.Tuners)).WithInUse(int32(s.TunersInUse)))
}

// reassertKnownStatus re-sends what the status already says when the tuner
// does not answer, so the apply releases none of it.
func reassertKnownStatus(st *clustarrac.IPTVProviderStatusApplyConfiguration, p *clustarrv1.IPTVProvider) {
	cur := p.Status
	if pl := cur.Playlist; pl != nil {
		ac := clustarrac.PlaylistStatus().WithEntries(pl.Entries).WithGroups(pl.Groups).WithCandidates(pl.Candidates)
		if pl.FetchedAt != nil {
			ac.WithFetchedAt(*pl.FetchedAt)
		}
		if pl.Hash != "" {
			ac.WithHash(pl.Hash)
		}
		if pl.Error != "" {
			ac.WithError(pl.Error)
		}
		st.WithPlaylist(ac)
	}
	if g := cur.Guide; g != nil {
		ac := clustarrac.GuideStatus().WithChannels(g.Channels).WithProgrammes(g.Programmes)
		if g.FetchedAt != nil {
			ac.WithFetchedAt(*g.FetchedAt)
		}
		if g.Error != "" {
			ac.WithError(g.Error)
		}
		st.WithGuide(ac)
	}
	if a := cur.Account; a != nil {
		ac := clustarrac.AccountStatus().WithMaxConnections(a.MaxConnections).WithActiveConnections(a.ActiveConnections)
		if a.Status != "" {
			ac.WithStatus(a.Status)
		}
		if a.ExpiresAt != nil {
			ac.WithExpiresAt(*a.ExpiresAt)
		}
		if a.FetchedAt != nil {
			ac.WithFetchedAt(*a.FetchedAt)
		}
		if a.Error != "" {
			ac.WithError(a.Error)
		}
		st.WithAccount(ac)
	}
	if lu := cur.Lineup; lu != nil {
		ac := clustarrac.LineupStatus().WithActive(lu.Active).WithUnmapped(lu.Unmapped).WithMissing(lu.Missing)
		if lu.First != "" {
			ac.WithFirst(lu.First).WithLast(lu.Last)
		}
		st.WithLineup(ac)
	}
	if t := cur.Tuners; t != nil {
		st.WithTuners(clustarrac.TunerStatus().WithTotal(t.Total).WithInUse(t.InUse))
	}
	if g := cur.PlexGuide; g != nil && p.Spec.PlexGuide != nil {
		ac := clustarrac.PlexGuideStatus().WithStations(g.Stations).WithPaired(g.Paired).WithAmbiguous(g.Ambiguous).
			WithTaken(g.Taken).WithTimeshifted(g.Timeshifted).WithNotInLineup(g.NotInLineup).WithOff(g.Off)
		if g.LineupID != "" {
			ac.WithLineupID(g.LineupID)
		}
		if g.Title != "" {
			ac.WithTitle(g.Title)
		}
		if g.FetchedAt != nil {
			ac.WithFetchedAt(*g.FetchedAt)
		}
		if g.Error != "" {
			ac.WithError(g.Error)
		}
		st.WithPlexGuide(ac)
	}
	if g := cur.GuideSources; g != nil && g.Plex != nil && p.Spec.GuideSources != nil && p.Spec.GuideSources.Plex != nil {
		ac := clustarrac.PlexSourceStatus().WithLineups(g.Plex.Lineups).WithStations(g.Plex.Stations).WithGrids(g.Plex.Grids)
		if g.Plex.FetchedAt != nil {
			ac.WithFetchedAt(*g.Plex.FetchedAt)
		}
		if g.Plex.HoldUntil != nil {
			ac.WithHoldUntil(*g.Plex.HoldUntil)
		}
		if g.Plex.Error != "" {
			ac.WithError(g.Plex.Error)
		}
		st.WithGuideSources(clustarrac.GuideSourcesStatus().WithPlex(ac))
	}
}

// plexGuideStatus is the snapshot's Plex lineup and how the channels
// paired with it.
func plexGuideStatus(s tuner.Snapshot) *clustarrac.PlexGuideStatusApplyConfiguration {
	counts := map[plexguide.Match]int32{}
	for _, c := range s.Channels {
		if c.Plex != nil {
			counts[c.Plex.Match]++
		}
	}
	ac := clustarrac.PlexGuideStatus().WithStations(int32(s.Plex.Stations)).
		WithPaired(counts[plexguide.Manual] + counts[plexguide.Automatic]).WithAmbiguous(counts[plexguide.Ambiguous]).
		WithTaken(counts[plexguide.Taken]).WithTimeshifted(counts[plexguide.Timeshifted]).
		WithNotInLineup(counts[plexguide.NotInLineup]).WithOff(counts[plexguide.Off])
	if s.Plex.LineupID != "" {
		ac.WithLineupID(s.Plex.LineupID)
	}
	if s.Plex.Title != "" {
		ac.WithTitle(s.Plex.Title)
	}
	if !s.Plex.FetchedAt.IsZero() {
		ac.WithFetchedAt(metav1.NewTime(s.Plex.FetchedAt))
	}
	if s.Plex.Error != "" {
		ac.WithError(s.Plex.Error)
	}
	return ac
}

// plexGuideCondition is PlexGuideReady: the lineup held, its last fetch's
// failure, or none yet; carried over from the status when the tuner does
// not answer.
func plexGuideCondition(p *clustarrv1.IPTVProvider, s tuner.Snapshot, ok bool) metav1.Condition {
	c := metav1.Condition{Type: clustarrv1.ConditionPlexGuideReady}
	switch {
	case !ok:
		if cur := meta.FindStatusCondition(p.Status.Conditions, clustarrv1.ConditionPlexGuideReady); cur != nil {
			return *cur
		}
		c.Status, c.Reason, c.Message = metav1.ConditionUnknown, clustarrv1.ReasonLineupPending, "the tuner has not answered yet"
	case s.Plex.Loaded && s.Plex.Error != "":
		c.Status, c.Reason, c.Message = metav1.ConditionTrue, clustarrv1.ReasonLineupFailed, "serving the last good lineup: "+s.Plex.Error
	case s.Plex.Loaded:
		c.Status, c.Reason = metav1.ConditionTrue, clustarrv1.ReasonLineupFetched
		c.Message = fmt.Sprintf("%s: %d stations", s.Plex.Title, s.Plex.Stations)
	case s.Plex.Error != "":
		c.Status, c.Reason, c.Message = metav1.ConditionFalse, clustarrv1.ReasonLineupFailed, s.Plex.Error
	default:
		c.Status, c.Reason, c.Message = metav1.ConditionFalse, clustarrv1.ReasonLineupPending, "the Plex lineup has not been fetched yet"
	}
	return c
}
