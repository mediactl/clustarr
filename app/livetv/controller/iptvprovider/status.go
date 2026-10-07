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

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"

	clustarrac "github.com/mediactl/clustarr/api/applyconfiguration/clustarr/clustarr/v1alpha1"
	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/tuner"
	"github.com/mediactl/clustarr/pkg/iptv/hdhr"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// writeStatus renders the provider's whole status, on every path, from the
// tuner's snapshot -- or, when the tuner does not answer for it, from what
// the status already says, so an early return never releases a field
// (CLAUDE.md's SSA gotchas).
func (r *Reconciler) writeStatus(ctx context.Context, p *clustarrv1.IPTVProvider, address string, valid metav1.Condition, disabled bool) error {
	_, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerLiveTV, render(p, address, r.Tuner, valid, disabled))
	return err
}

func render(p *clustarrv1.IPTVProvider, address string, tn Tuner, valid metav1.Condition, disabled bool) *clustarrac.IPTVProviderApplyConfiguration {
	key := types.NamespacedName{Namespace: p.Namespace, Name: p.Name}
	snap, ok := tn.Snapshot(key)
	if disabled {
		ok = false
	}
	if address == "" {
		address = p.Status.Address
	}
	st := clustarrac.IPTVProviderStatus().
		WithObservedGeneration(p.Generation).
		WithDeviceID(hdhr.DeviceID(p.Namespace, p.Name))
	if address != "" {
		st.WithAddress(address)
		if p.Spec.EPGSourceOrDefault() == clustarrv1.EPGSourceXEPG {
			st.WithGuideURL("http://" + address + "/livetv/" + p.Namespace + "/" + p.Name + "/xmltv.xml")
		}
	}
	if ok {
		fromSnapshot(st, snap, p.Spec.Tuners)
	} else {
		reassertKnownStatus(st, p)
	}

	conds := slices.Clone(p.Status.Conditions)
	set := func(c metav1.Condition) { c.ObservedGeneration = p.Generation; meta.SetStatusCondition(&conds, c) }
	set(valid)
	playlist, guides := fetchConditions(p, snap, ok)
	set(playlist)
	set(guides)
	ready := metav1.Condition{Type: clustarrv1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", Message: "the tuner serves this provider"}
	pms := p.Spec.EPGSourceOrDefault() == clustarrv1.EPGSourcePMS
	switch {
	case disabled:
		ready = metav1.Condition{Type: clustarrv1.ConditionReady, Status: metav1.ConditionFalse, Reason: clustarrv1.ReasonDisabled, Message: "spec.enabled is false"}
	case valid.Status != metav1.ConditionTrue:
		ready = metav1.Condition{Type: clustarrv1.ConditionReady, Status: metav1.ConditionFalse, Reason: valid.Reason, Message: valid.Message}
	case playlist.Status != metav1.ConditionTrue:
		ready = metav1.Condition{Type: clustarrv1.ConditionReady, Status: metav1.ConditionFalse, Reason: "PlaylistNotFetched", Message: playlist.Message}
	case !pms && guides.Status != metav1.ConditionTrue:
		ready = metav1.Condition{Type: clustarrv1.ConditionReady, Status: metav1.ConditionFalse, Reason: "GuidesNotFetched", Message: guides.Message}
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

// fetchConditions are PlaylistFetched and GuidesFetched: from the snapshot,
// else carried over from the status.
func fetchConditions(p *clustarrv1.IPTVProvider, snap tuner.Snapshot, ok bool) (playlist, guides metav1.Condition) {
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
	var pending, failed []string
	for _, g := range snap.Guides {
		switch {
		case g.FetchedAt.IsZero() && g.Error != "":
			failed = append(failed, g.Error)
		case g.FetchedAt.IsZero():
			pending = append(pending, g.Name)
		case g.Error != "":
			failed = append(failed, "serving the last good copy: "+g.Error)
		}
	}
	guides = metav1.Condition{
		Type: clustarrv1.ConditionGuidesFetched, Status: metav1.ConditionTrue, Reason: "Fetched",
		Message: fmt.Sprintf("%d guides", len(snap.Guides)),
	}
	switch {
	case len(snap.Guides) == 0:
		guides.Reason, guides.Message = "NoGuides", "no guides: every channel takes a dummy guide"
	case len(failed) > 0 && len(pending) == 0 && !anyFetched(snap.Guides):
		guides = metav1.Condition{Type: clustarrv1.ConditionGuidesFetched, Status: metav1.ConditionFalse, Reason: "FetchFailed", Message: clampMessage(strings.Join(failed, "; "))}
	case len(pending) > 0:
		guides = metav1.Condition{Type: clustarrv1.ConditionGuidesFetched, Status: metav1.ConditionFalse, Reason: "Pending", Message: "not fetched yet: " + strings.Join(pending, ", ")}
	case len(failed) > 0:
		guides.Reason, guides.Message = "StaleAfterError", clampMessage(strings.Join(failed, "; "))
	}
	return playlist, guides
}

func anyFetched(gs []tuner.GuideState) bool {
	for _, g := range gs {
		if !g.FetchedAt.IsZero() {
			return true
		}
	}
	return false
}

// clampMessage keeps a condition message within the apiserver's 32 KiB.
func clampMessage(s string) string {
	const limit = 4096
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}

func fromSnapshot(st *clustarrac.IPTVProviderStatusApplyConfiguration, s tuner.Snapshot, tuners int32) {
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
	for i, g := range s.Guides {
		if i == 16 { // the CRD's MaxItems; the spec allows no more
			break
		}
		gs := clustarrac.GuideStatus().WithName(g.Name).WithChannels(int32(g.Channels)).WithProgrammes(int32(g.Programmes))
		if !g.FetchedAt.IsZero() {
			gs.WithFetchedAt(metav1.NewTime(g.FetchedAt))
		}
		if g.Error != "" {
			gs.WithError(g.Error)
		}
		st.WithGuides(gs)
	}
	lu := clustarrac.LineupStatus().WithActive(int32(s.Lineup.Active)).WithUnmapped(int32(s.Lineup.Unmapped)).WithMissing(int32(s.Lineup.Missing))
	if s.Lineup.Hash != "" {
		lu.WithHash(s.Lineup.Hash)
	}
	st.WithLineup(lu)
	if s.GuideHash != "" {
		st.WithGuideHash(s.GuideHash)
	}
	st.WithTuners(clustarrac.TunerStatus().WithTotal(tuners).WithInUse(int32(s.TunersInUse)))
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
	for _, g := range cur.Guides {
		ac := clustarrac.GuideStatus().WithName(g.Name).WithChannels(g.Channels).WithProgrammes(g.Programmes)
		if g.FetchedAt != nil {
			ac.WithFetchedAt(*g.FetchedAt)
		}
		if g.Error != "" {
			ac.WithError(g.Error)
		}
		st.WithGuides(ac)
	}
	if lu := cur.Lineup; lu != nil {
		ac := clustarrac.LineupStatus().WithActive(lu.Active).WithUnmapped(lu.Unmapped).WithMissing(lu.Missing)
		if lu.Hash != "" {
			ac.WithHash(lu.Hash)
		}
		st.WithLineup(ac)
	}
	if cur.GuideHash != "" {
		st.WithGuideHash(cur.GuideHash)
	}
	if t := cur.Tuners; t != nil {
		st.WithTuners(clustarrac.TunerStatus().WithTotal(t.Total).WithInUse(t.InUse))
	}
}
