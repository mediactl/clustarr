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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"

	clustarrac "github.com/mediactl/clustarr/api/applyconfiguration/clustarr/v1alpha1"
	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/tuner"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// writeStatus renders the provider's whole status, on every path, from the
// tuner's snapshot -- or, when the tuner does not answer for it, from what
// the status already says, so an early return never releases a field
// (CLAUDE.md's SSA gotchas).
func (r *Reconciler) writeStatus(ctx context.Context, p *clustarrv1.IPTVProvider, pl plan, valid metav1.Condition, disabled bool) error {
	_, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerLiveTV, render(p, r.Address, pl, r.Tuner, valid, disabled))
	return err
}

func render(p *clustarrv1.IPTVProvider, address string, pl plan, tn Tuner, valid metav1.Condition, disabled bool) *clustarrac.IPTVProviderApplyConfiguration {
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
	if ok {
		fromSnapshot(st, snap)
	} else {
		reassertKnownStatus(st, p)
	}
	withBlocks(st, p, pl, snap, ok)

	conds := slices.Clone(p.Status.Conditions)
	set := func(c metav1.Condition) { c.ObservedGeneration = p.Generation; meta.SetStatusCondition(&conds, c) }
	set(valid)
	playlist, guide := fetchConditions(p, snap, ok)
	set(playlist)
	set(guide)
	ready := metav1.Condition{Type: clustarrv1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", Message: "the tuner serves this provider"}
	pms := p.Spec.EPGSourceOrDefault() == clustarrv1.EPGSourcePMS
	switch {
	case disabled:
		ready = metav1.Condition{Type: clustarrv1.ConditionReady, Status: metav1.ConditionFalse, Reason: clustarrv1.ReasonDisabled, Message: "spec.enabled is false"}
	case valid.Status != metav1.ConditionTrue:
		ready = metav1.Condition{Type: clustarrv1.ConditionReady, Status: metav1.ConditionFalse, Reason: valid.Reason, Message: valid.Message}
	case pl.reason != "":
		ready = metav1.Condition{Type: clustarrv1.ConditionReady, Status: metav1.ConditionFalse, Reason: pl.reason, Message: pl.message + "; the last good blocks are kept"}
	case playlist.Status != metav1.ConditionTrue:
		ready = metav1.Condition{Type: clustarrv1.ConditionReady, Status: metav1.ConditionFalse, Reason: "PlaylistNotFetched", Message: playlist.Message}
	case !pms && guide.Status != metav1.ConditionTrue:
		ready = metav1.Condition{Type: clustarrv1.ConditionReady, Status: metav1.ConditionFalse, Reason: "GuidesNotFetched", Message: guide.Message}
	}
	set(ready)
	for _, c := range conds {
		switch c.Type {
		case clustarrv1.ConditionReady, clustarrv1.ConditionValid, clustarrv1.ConditionPlaylistFetched, clustarrv1.ConditionGuidesFetched:
			st.WithConditions(metav1ac.Condition().WithType(c.Type).WithStatus(c.Status).WithReason(c.Reason).
				WithMessage(c.Message).WithLastTransitionTime(c.LastTransitionTime).WithObservedGeneration(c.ObservedGeneration))
		}
	}
	return clustarrac.IPTVProvider(p.Name, p.Namespace).WithStatus(st)
}

// withBlocks writes the planned blocks, each with its lineup and guide hashes
// from the snapshot, or, when the tuner does not answer, from the status.
func withBlocks(st *clustarrac.IPTVProviderStatusApplyConfiguration, p *clustarrv1.IPTVProvider, pl plan, snap tuner.Snapshot, ok bool) {
	rendered := map[string]tuner.BlockState{}
	if ok {
		for _, b := range snap.Blocks {
			rendered[b.Start] = b
		}
	}
	known := map[string]clustarrv1.BlockStatus{}
	for _, b := range p.Status.Blocks {
		known[b.Start] = b
	}
	for _, b := range pl.blocks {
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
		if b.Owner != "" {
			ac.WithOwner(b.Owner)
		}
		// A tuner that has not rendered a playlist yet (a manager just
		// started, before its cache loads) has no hashes: the status keeps
		// the last ones, or cluster-plex would save every block's map and
		// reload its guide again.
		lineupHash, guideHash := known[b.Start].LineupHash, known[b.Start].GuideHash
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
	if p.Spec.EPGSourceOrDefault() == clustarrv1.EPGSourcePMS {
		return playlist, metav1.Condition{Type: clustarrv1.ConditionGuidesFetched, Status: metav1.ConditionTrue, Reason: "NotUsed", Message: "epgSource is PMS"}
	}
	if p.Spec.TypeOrDefault() == clustarrv1.ProviderTypeM3U && p.Spec.GuideURL == "" {
		return playlist, metav1.Condition{Type: clustarrv1.ConditionGuidesFetched, Status: metav1.ConditionTrue, Reason: "NoGuide",
			Message: "the M3U provider names no guideURL: every channel has a dummy guide"}
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
}
