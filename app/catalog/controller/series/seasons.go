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

package series

import (
	"sort"

	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// SeasonOverrides is s' spec.seasons entries that set monitored, by season
// number.
func SeasonOverrides(s *catalogv1alpha1.Series) map[int32]bool {
	out := make(map[int32]bool, len(s.Spec.Seasons))
	for _, ssn := range s.Spec.Seasons {
		if ssn.Monitored != nil {
			out[ssn.Number] = *ssn.Monitored
		}
	}
	return out
}

// MonitoredChange is one Episode whose spec.monitored a season override
// still has to set.
type MonitoredChange struct {
	Name      string
	Monitored bool
}

// SeasonCascade is Sonarr's season toggle: for every season whose override
// differs from the one status.seasons[].appliedMonitored says was last
// applied, each of its episodes whose spec.monitored is not the override
// yet. A season already applied yields nothing, so an episode toggled on its
// own after its season keeps its own flag. Changes come sorted by name.
func SeasonCascade(s *catalogv1alpha1.Series, episodes []catalogv1alpha1.Episode) []MonitoredChange {
	overrides := SeasonOverrides(s)
	pending := make(map[int32]bool, len(overrides))
	for n, v := range overrides {
		pending[n] = v
	}
	for _, st := range s.Status.Seasons {
		if v, ok := pending[st.Number]; ok && st.AppliedMonitored != nil && *st.AppliedMonitored == v {
			delete(pending, st.Number)
		}
	}
	var out []MonitoredChange
	for _, ep := range episodes {
		v, ok := pending[ep.Spec.SeasonNumber]
		if !ok || ptr.Deref(ep.Spec.Monitored, true) == v {
			continue
		}
		out = append(out, MonitoredChange{Name: ep.Name, Monitored: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// withApplied records on seasons which override each one has had applied:
// overrides for a season this pass applied (cascaded is true), otherwise
// what status.seasons already said, so a failed cascade is retried and a
// season whose override was removed keeps no stale record.
func withApplied(seasons []catalogv1alpha1.SeasonStatus, s *catalogv1alpha1.Series, cascaded bool) []catalogv1alpha1.SeasonStatus {
	overrides := SeasonOverrides(s)
	prior := make(map[int32]*bool, len(s.Status.Seasons))
	for _, st := range s.Status.Seasons {
		prior[st.Number] = st.AppliedMonitored
	}
	out := make([]catalogv1alpha1.SeasonStatus, len(seasons))
	for i, st := range seasons {
		st.AppliedMonitored = nil
		if v, ok := overrides[st.Number]; ok {
			switch {
			case cascaded:
				st.AppliedMonitored = new(v)
			case prior[st.Number] != nil:
				st.AppliedMonitored = new(*prior[st.Number])
			}
		}
		out[i] = st
	}
	return out
}
