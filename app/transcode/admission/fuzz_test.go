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
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
)

// FuzzLedgerNeverOverAdmits is §5.17's fuzz: random fresh and stale
// Observes, passes, grant takes, confirms, releases, expiries and a crash
// that rebuilds from the "apiserver" (the last applied view of each file).
// After every pass: in flight plus live dispatch grants never exceed a
// class's slots or the profile's maxConcurrent, and a file whose applied
// status has no block holds nothing.
func FuzzLedgerNeverOverAdmits(f *testing.F) {
	f.Add([]byte{0, 0, 0, 1, 1, 0, 2, 0, 3, 0, 1, 0, 7, 0, 1, 0})
	f.Add([]byte{0, 2, 0, 3, 1, 0, 2, 2, 2, 3, 3, 2, 6, 2, 5, 0, 1, 0})
	// Six files planned, a pass, every grant taken, a crash, a pass.
	f.Add([]byte{0, 0, 0, 1, 0, 2, 0, 3, 0, 4, 0, 5, 1, 0, 2, 0, 2, 1, 2, 2, 2, 3, 2, 4, 2, 5, 7, 0, 1, 0})
	// A clear (no block) after a dispatch, then a pass and another file.
	f.Add([]byte{0, 0, 1, 0, 2, 0, 8, 0, 0, 1, 1, 0, 2, 1, 1, 0})
	// A clear while a dispatch grant is outstanding.
	f.Add([]byte{0, 0, 1, 0, 8, 0, 1, 0})
	// Two passes while the first pass's grants are outstanding.
	f.Add([]byte{0, 0, 0, 1, 0, 2, 0, 3, 1, 0, 1, 0})
	f.Fuzz(func(t *testing.T, ops []byte) {
		slots := map[string]int32{"cpu": 2, "nvidia": 1}
		l, clk := newTestLedger(t, slots, 4)
		in := passIn()
		in.Profiles["hevc"] = ProfileView{Name: "hevc", UID: profileUID, MaxConcurrent: 2}
		in.GPUNodes = map[transcodev1alpha1.Hardware]bool{nvidia: true}
		applied := map[string]FileView{} // what the apiserver holds
		seq := map[string]int64{}
		for i := 0; i+1 < len(ops); i += 2 {
			uid := string(rune('a' + ops[i+1]%6))
			switch ops[i] % 9 {
			case 0: // the loop writes Planned (fresh)
				seq[uid]++
				applied[uid] = fv(uid, seq[uid], planned)
				l.Observe(applied[uid])
			case 1:
				l.Pass(in)
			case 2: // a pass takes its grant and its apply lands (Queued)
				if g, ok := l.TakeGrant(types.UID(uid)); ok && g.Grant == transcodeplan.GrantDispatch {
					seq[uid]++
					v := fv(uid, seq[uid], queued)
					v.Class = g.Class
					applied[uid] = v
					l.Observe(v)
					l.Confirm(types.UID(uid), seq[uid])
				}
			case 3: // a worker claims it
				if v, ok := applied[uid]; ok && v.Phase == queued {
					v.Phase = running
					applied[uid] = v
					l.Observe(v)
				}
			case 4: // the planner releases (panic, ineligible)
				l.Release(types.UID(uid))
			case 5:
				clk.Advance(grantTTL + time.Second)
			case 6: // a stale cached view
				if v, ok := applied[uid]; ok && v.Seq > 1 {
					stale := v
					stale.Seq--
					stale.Phase = planned
					l.Observe(stale)
				}
			case 7: // crash and rebuild from the apiserver
				l = NewLedger(Options{Slots: slots, Window: 4, Clock: clk})
				for _, v := range applied {
					l.Observe(v)
				}
				l.markRebuilt()
			case 8: // the block is cleared (the file left the profile)
				if v, ok := applied[uid]; ok {
					seq[uid]++
					v.Seq, v.Phase = seq[uid], ""
					applied[uid] = v
					l.Observe(v)
				}
			}
			if ops[i]%9 == 1 {
				for class, n := range slots {
					c := transcodev1alpha1.Hardware(class)
					require.LessOrEqual(t, l.inFlight(c)+l.dispatchGrants(c), int(n), "class %s", class)
				}
				require.LessOrEqual(t, l.inFlightProfile("hevc")+l.dispatchGrantsProfile("hevc"), 2, "maxConcurrent")
				for uid, v := range applied {
					if v.Phase == "" {
						require.NotEqual(t, transcodeplan.GrantDispatch, l.Snapshot(types.UID(uid)).Grant,
							"%s has no block and holds a dispatch grant", uid)
						require.Empty(t, l.phaseOf(types.UID(uid)), "%s has no block in the ledger", uid)
					}
				}
			}
		}
	})
}
