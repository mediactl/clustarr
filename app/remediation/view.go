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

package remediation

import (
	"maps"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/pkg/records"
)

// View is everything a planner may read, read-only. Plan writes only out.
type View struct {
	File  *catalogv1alpha1.MediaFile       // the cached object this pass planned from
	Prev  *catalogv1alpha1.MediaFileStatus // the stored status, File.Status
	Draft *catalogv1alpha1.MediaFileStatus // the status after the earlier planners in Order
	Now   metav1.Time                      // UTC, whole seconds
	Stale bool                             // the cache has not caught up with the loop's own last apply
	// Main is the takeover the probe planner decided this pass, set once it
	// has planned; later planners read SpecPath (D-F3-4).
	Main     *MainIntent
	seqFloor int64
}

// Labels returns File's labels overlaid with MirrorLabels over Draft's
// mediaInfo: the labels this pass's main apply will leave. Every profile
// selection reads these, never File.Labels.
func (v *View) Labels() map[string]string {
	out := maps.Clone(v.File.Labels)
	if out == nil {
		out = map[string]string{}
	}
	if v.Draft == nil || v.Draft.MediaInfo == nil {
		return out
	}
	for _, k := range mediafile.LoopLabelKeys {
		delete(out, k)
	}
	maps.Copy(out, mediafile.MirrorLabels(v.File.Spec.MediaRef.Kind, v.File.Spec.Quality, v.Draft.MediaInfo, v.original()))
	return out
}

func (v *View) original() bool {
	if v.Main != nil && v.Main.Swap {
		return false
	}
	return v.File.Spec.Original == nil || *v.File.Spec.Original
}

// SpecPath is spec.path as of this pass's main apply: a swap the probe
// planner incorporated moves it.
func (v *View) SpecPath() string {
	if v.Main != nil && v.Main.Path != "" {
		return v.Main.Path
	}
	return v.File.Spec.Path
}

// ProbeCurrent is (*MediaFile).ProbeCurrent on the draft (split §6.5.3).
func (v *View) ProbeCurrent() bool {
	return (&catalogv1alpha1.MediaFile{Status: *v.Draft}).ProbeCurrent()
}

// Issue returns the next sequence for a dispatch whose record holds
// recordSeq, and raises the floor, so two dispatches in one pass never share
// a number (§2.3, §4.6).
func (v *View) Issue(recordSeq int64) int64 {
	s := records.NextSeq(recordSeq, v.seqFloor, v.Now.Time)
	v.seqFloor = s
	return s
}
