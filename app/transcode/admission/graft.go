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

package admission

import (
	"maps"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/graftplan"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
)

// GraftFile is a file as the graft-slot count reads it (loop spec §5.13).
type GraftFile struct {
	UID             types.UID
	Key             types.NamespacedName
	Phase           catalogv1alpha1.GraftPhase
	Reason          string
	JobName         string
	DonorImportedAt time.Time
	JoinInFlight    bool // its transcode block carries an in-flight joinedGraft
	Granted         bool // it holds an outstanding grant of any kind
	OnBackoff       bool // an expired grant's backoff holds it
}

// holdsGraftSlot reports a block whose own Job holds a graft slot: Queued,
// or Running and not riding a transcode.
func holdsGraftSlot(f GraftFile) bool {
	return f.Phase == catalogv1alpha1.GraftPhaseQueued ||
		f.Phase == catalogv1alpha1.GraftPhaseRunning && f.Reason != graftplan.ReasonJoinedTranscode
}

// graftCandidate reports a standalone graft waiting for a slot: never one
// whose file's transcode carries the graft in flight (no standalone graft
// Job beside a transcode that grafts), nor one already granted or backing
// off.
func graftCandidate(f GraftFile) bool {
	return f.Phase == catalogv1alpha1.GraftPhaseWaiting && f.Reason == graftplan.ReasonWaitingForSlot &&
		!f.JoinInFlight && !f.Granted && !f.OnBackoff
}

// GraftInUse is a namespace's graft slots in use (§5.13): the union, by Job
// name, of its unfinished graft Jobs (jobs, by namespace) and its blocks
// that hold a slot, plus its outstanding graft-slot grants (grants, by
// namespace).
func GraftInUse(ns string, files []GraftFile, jobs map[string][]string, grants map[string]int) int {
	names := map[string]bool{}
	for _, j := range jobs[ns] {
		names[j] = true
	}
	extra := 0
	for _, f := range files {
		if f.Key.Namespace != ns || !holdsGraftSlot(f) {
			continue
		}
		if f.JobName == "" {
			extra++
			continue
		}
		names[f.JobName] = true
	}
	return len(names) + extra + grants[ns]
}

// GraftGrants picks the files that get a graft slot now: per namespace, up
// to limit (graftplan.DefaultConcurrency when <= 0) less GraftInUse, the
// candidates by donorImportedAt, then namespace/name.
func GraftGrants(files []GraftFile, jobs map[string][]string, grants map[string]int, limit int) []types.UID {
	if limit <= 0 {
		limit = graftplan.DefaultConcurrency
	}
	byNS := map[string][]GraftFile{}
	for _, f := range files {
		if graftCandidate(f) {
			byNS[f.Key.Namespace] = append(byNS[f.Key.Namespace], f)
		}
	}
	var out []types.UID
	for _, ns := range slices.Sorted(maps.Keys(byNS)) {
		c := byNS[ns]
		slices.SortFunc(c, func(a, b GraftFile) int {
			if n := a.DonorImportedAt.Compare(b.DonorImportedAt); n != 0 {
				return n
			}
			return strings.Compare(a.Key.String(), b.Key.String())
		})
		free := limit - GraftInUse(ns, files, jobs, grants)
		for i := 0; i < free && i < len(c); i++ {
			out = append(out, c[i].UID)
		}
	}
	return out
}

// grantGraftSlotsLocked is §5.7 step 9: the free graft slots of each
// namespace go to its waiting standalone grafts, each granted and enqueued
// at GrantPriority; the graft planner dispatches on the grant and confirms
// it.
func (l *Ledger) grantGraftSlotsLocked(in PassInputs, now time.Time) {
	files := make([]GraftFile, 0, len(l.files))
	grants := map[string]int{}
	for uid, e := range l.files {
		g := l.grants[uid]
		files = append(files, GraftFile{
			UID: uid, Key: e.view.Key, Phase: e.view.GraftPhase, Reason: e.view.GraftReason,
			JobName: e.view.GraftJobName, DonorImportedAt: e.view.DonorImportedAt, JoinInFlight: e.view.JoinedGraft,
			Granted: g != nil, OnBackoff: l.onBackoffLocked(uid, now),
		})
		if g != nil && g.kind == transcodeplan.GrantGraftSlot && (g.inUse || now.Before(g.expires)) {
			grants[e.view.Key.Namespace]++
		}
	}
	for _, uid := range GraftGrants(files, in.GraftJobs, grants, l.o.GraftConcurrency) {
		e := l.files[uid]
		l.grantLocked(uid, e.view.Key, transcodeplan.GrantGraftSlot, "", "", now)
	}
}
