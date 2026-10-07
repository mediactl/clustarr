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

package transcodejob

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAdmit(t *testing.T) {
	t0 := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	at := func(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }
	defaults := map[string]int32{"cpu": 2, "nvidia": 1, "intel": 1}

	tests := []struct {
		name    string
		queued  []Slot
		running []Slot
		budget  Budget
		want    []string
	}{
		{
			name:   "nothing queued admits nothing",
			budget: Budget{Slots: defaults},
			want:   nil,
		},
		{
			name: "higher priority first, within one tier's budget",
			queued: []Slot{
				{Key: "ns/low", Hardware: "cpu", Priority: 10, Created: at(0)},
				{Key: "ns/high", Hardware: "cpu", Priority: 90, Created: at(5)},
				{Key: "ns/mid", Hardware: "cpu", Priority: 50, Created: at(1)},
			},
			budget: Budget{Slots: defaults},
			want:   []string{"ns/high", "ns/mid"},
		},
		{
			name: "running jobs consume the budget",
			queued: []Slot{
				{Key: "ns/a", Hardware: "cpu", Priority: 50, Created: at(0)},
				{Key: "ns/b", Hardware: "cpu", Priority: 50, Created: at(1)},
			},
			running: []Slot{{Key: "ns/r", Hardware: "cpu"}},
			budget:  Budget{Slots: defaults},
			want:    []string{"ns/a"},
		},
		{
			name: "per-tier budgets are independent",
			queued: []Slot{
				{Key: "ns/gpu1", Hardware: "nvidia", Priority: 90, Created: at(0)},
				{Key: "ns/gpu2", Hardware: "nvidia", Priority: 80, Created: at(0)},
				{Key: "ns/qsv", Hardware: "intel", Priority: 70, Created: at(0)},
				{Key: "ns/cpu1", Hardware: "cpu", Priority: 10, Created: at(0)},
			},
			budget: Budget{Slots: defaults},
			// gpu2 is blocked by the one NVIDIA slot but does not hold back
			// the lower-priority CPU encode behind it.
			want: []string{"ns/gpu1", "ns/qsv", "ns/cpu1"},
		},
		{
			name: "a tier with zero slots never admits",
			queued: []Slot{
				{Key: "ns/gpu", Hardware: "nvidia", Priority: 100, Created: at(0)},
				{Key: "ns/cpu", Hardware: "cpu", Priority: 1, Created: at(0)},
			},
			budget: Budget{Slots: map[string]int32{"cpu": 1, "nvidia": 0}},
			want:   []string{"ns/cpu"},
		},
		{
			name: "a tier the budget does not name is a zero budget",
			queued: []Slot{
				{Key: "ns/qsv", Hardware: "intel", Priority: 100, Created: at(0)},
			},
			budget: Budget{Slots: map[string]int32{"cpu": 2}},
			want:   nil,
		},
		{
			name: "over budget after a --slots reduction admits nothing",
			queued: []Slot{
				{Key: "ns/q", Hardware: "cpu", Priority: 100, Created: at(0)},
			},
			running: []Slot{
				{Key: "ns/r1", Hardware: "cpu"}, {Key: "ns/r2", Hardware: "cpu"}, {Key: "ns/r3", Hardware: "cpu"},
			},
			budget: Budget{Slots: map[string]int32{"cpu": 1}},
			want:   nil,
		},
		{
			name: "ties on priority break by creation time, oldest first",
			queued: []Slot{
				{Key: "ns/newer", Hardware: "cpu", Priority: 50, Created: at(10)},
				{Key: "ns/older", Hardware: "cpu", Priority: 50, Created: at(1)},
			},
			budget: Budget{Slots: map[string]int32{"cpu": 1}},
			want:   []string{"ns/older"},
		},
		{
			name: "ties on priority and creation time break by key",
			queued: []Slot{
				{Key: "ns/zulu", Hardware: "cpu", Priority: 50, Created: at(0)},
				{Key: "ns/alpha", Hardware: "cpu", Priority: 50, Created: at(0)},
				{Key: "other/alpha", Hardware: "cpu", Priority: 50, Created: at(0)},
			},
			budget: Budget{Slots: map[string]int32{"cpu": 2}},
			want:   []string{"ns/alpha", "ns/zulu"},
		},
		{
			name: "per-profile limit counts running and just-admitted jobs",
			queued: []Slot{
				{Key: "ns/uhd1", Hardware: "cpu", Profile: "uhd", Priority: 90, Created: at(0)},
				{Key: "ns/uhd2", Hardware: "cpu", Profile: "uhd", Priority: 80, Created: at(0)},
				{Key: "ns/hd1", Hardware: "cpu", Profile: "hd", Priority: 10, Created: at(0)},
			},
			budget: Budget{Slots: map[string]int32{"cpu": 3}, ProfileLimits: map[string]int32{"uhd": 1}},
			want:   []string{"ns/uhd1", "ns/hd1"},
		},
		{
			name: "per-profile limit already reached by running jobs",
			queued: []Slot{
				{Key: "ns/uhd", Hardware: "nvidia", Profile: "uhd", Priority: 90, Created: at(0)},
			},
			running: []Slot{{Key: "ns/r", Hardware: "cpu", Profile: "uhd"}},
			budget:  Budget{Slots: defaults, ProfileLimits: map[string]int32{"uhd": 1}},
			want:    nil,
		},
		{
			name: "a non-positive profile limit means unlimited",
			queued: []Slot{
				{Key: "ns/a", Hardware: "cpu", Profile: "p", Priority: 1, Created: at(0)},
				{Key: "ns/b", Hardware: "cpu", Profile: "p", Priority: 1, Created: at(1)},
			},
			budget: Budget{Slots: defaults, ProfileLimits: map[string]int32{"p": 0}},
			want:   []string{"ns/a", "ns/b"},
		},
		{
			name: "a duplicated key is admitted once",
			queued: []Slot{
				{Key: "ns/a", Hardware: "cpu", Priority: 1, Created: at(0)},
				{Key: "ns/a", Hardware: "cpu", Priority: 1, Created: at(0)},
			},
			budget: Budget{Slots: defaults},
			want:   []string{"ns/a"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Admit(tc.queued, tc.running, tc.budget)
			var keys []string
			for _, s := range got {
				keys = append(keys, s.Key)
			}
			assert.Equal(t, tc.want, keys)

			// Input order must not matter: reverse the queue and re-run.
			rev := make([]Slot, len(tc.queued))
			for i, q := range tc.queued {
				rev[len(tc.queued)-1-i] = q
			}
			var revKeys []string
			for _, s := range Admit(rev, tc.running, tc.budget) {
				revKeys = append(revKeys, s.Key)
			}
			assert.Equal(t, keys, revKeys, "Admit depends on input order")
		})
	}
}

func TestAdmitDoesNotMutateItsInput(t *testing.T) {
	queued := []Slot{
		{Key: "ns/b", Hardware: "cpu", Priority: 1},
		{Key: "ns/a", Hardware: "cpu", Priority: 9},
	}
	Admit(queued, nil, Budget{Slots: map[string]int32{"cpu": 1}})
	assert.Equal(t, "ns/b", queued[0].Key)
}
