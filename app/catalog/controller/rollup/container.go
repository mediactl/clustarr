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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// ContainerChild is what a container reads from one child -- an Artist
// from an Album, an Author from a Book, a Comic from an Issue -- to fold
// into its ContainerRollup (2026-11-10 design §4.1).
type ContainerChild struct {
	// Monitored is the child's own spec.monitored.
	Monitored bool
	// Phase is the child's status.phase.
	Phase commonv1.LeafPhase
	// HasFile is the child's status.hasFile.
	HasFile bool
	// ReleaseDate is the child's release date, nil when unknown.
	ReleaseDate *metav1.Time
}

// Container folds a container's children into the ContainerRollup every
// container status carries, the Series' series.Rollup for the non-video
// kinds. Phase is Pending until the container's metadata is fetched,
// Unmonitored while its spec.monitored is false, else Ready. The counts
// read the children's phases: Wanted is missing, Downloading or Delayed is
// downloading, CutoffUnmet is cutoff unmet. The release dates follow
// Sonarr's series statistics: the next release is the earliest date at or
// after now and the previous the latest before it, both over MONITORED
// children with a date only.
func Container(monitored, metadataReady bool, children []ContainerChild, now time.Time) catalogv1alpha1.ContainerRollup {
	out := catalogv1alpha1.ContainerRollup{Phase: catalogv1alpha1.ContainerPhaseReady}
	switch {
	case !metadataReady:
		out.Phase = catalogv1alpha1.ContainerPhasePending
	case !monitored:
		out.Phase = catalogv1alpha1.ContainerPhaseUnmonitored
	}
	for _, c := range children {
		switch c.Phase {
		case commonv1.LeafPhaseWanted:
			out.MissingChildCount++
		case commonv1.LeafPhaseDownloading, commonv1.LeafPhaseDelayed:
			out.DownloadingChildCount++
		case commonv1.LeafPhaseCutoffUnmet:
			out.CutoffUnmetChildCount++
		}
		if c.ReleaseDate == nil || !c.Monitored {
			continue
		}
		at := c.ReleaseDate.Time
		if at.Before(now) {
			out.PreviousReleaseDate = later(out.PreviousReleaseDate, at)
			continue
		}
		out.NextReleaseDate = earlier(out.NextReleaseDate, at)
	}
	return out
}

func earlier(cur *metav1.Time, at time.Time) *metav1.Time {
	if cur == nil || at.Before(cur.Time) {
		t := metav1.NewTime(at)
		return &t
	}
	return cur
}

func later(cur *metav1.Time, at time.Time) *metav1.Time {
	if cur == nil || at.After(cur.Time) {
		t := metav1.NewTime(at)
		return &t
	}
	return cur
}
