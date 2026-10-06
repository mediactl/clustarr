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

package rollup

import (
	"strings"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/quality"
)

// AudioStateFor is status.audio (anime dual-audio spec §5.3): the profile's
// wanted audio languages resolved to tags ("original" to originalTag, an
// unknown one dropped), the file's probed ones, and what is missing -- only
// when every probed track is known, since an untagged track may be the
// wanted language. Graft reads "none" while the profile grafts and no graft
// has run; phase 4 moves it on. Nil when there is no file or no audio
// policy.
func AudioStateFor(p *quality.Profile, originalTag string, mf *catalogv1alpha1.MediaFile) *catalogv1alpha1.AudioState {
	if p == nil || len(p.AudioLanguages) == 0 || mf == nil {
		return nil
	}
	st := &catalogv1alpha1.AudioState{}
	for _, l := range p.AudioLanguages {
		if l == "original" {
			l = originalTag
		}
		if t, ok := lang.Normalize(l); ok {
			st.Wanted = append(st.Wanted, string(t))
		}
	}
	st.Present = ProbedAudioLanguages(mf)
	if st.Present != nil {
		base := func(t string) string { b, _, _ := strings.Cut(t, "-"); return strings.ToLower(b) }
		for _, w := range st.Wanted {
			found := false
			for _, h := range st.Present {
				if base(h) == base(w) {
					found = true
					break
				}
			}
			if !found {
				st.Missing = append(st.Missing, w)
			}
		}
	}
	if p.AudioGraft {
		st.Graft = "none"
	}
	return st
}

// AudioStateAC renders status.audio, the one renderer the Movie and Episode
// reconcilers share for their happy paths and early returns alike.
func AudioStateAC(a *catalogv1alpha1.AudioState) *catalogac.AudioStateApplyConfiguration {
	ac := catalogac.AudioState().WithWanted(a.Wanted...).WithPresent(a.Present...).WithMissing(a.Missing...)
	if a.Graft != "" {
		ac = ac.WithGraft(a.Graft)
	}
	if a.Reason != "" {
		ac = ac.WithReason(a.Reason)
	}
	return ac
}
