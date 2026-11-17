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

package importplan

import (
	"fmt"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
)

// A series is kept in one shape of picture (Series.WantedAspect, owner
// 2026-11-17: widescreen unless it was made in 4:3, and a season one
// shape). A release's shape is in no name; it is known when its file is
// probed, which is here. So the rule is import's:
//
//   - an item with no file takes the file that came, whatever its shape:
//     a wrong one is better than none, and the Episode then reads
//     WrongAspectRatio and is searched for again;
//   - a file of the wrong shape replaces no file an item has -- it is the
//     release's fault, so the release is blocklisted for the owner and the
//     next is searched for (the grab plan stops after MaxAspectTries);
//   - a file of the wanted shape replaces a file of the wrong one whatever
//     their qualities, a transcode included (aspectRefit), as a file
//     inside the bitrate range replaces one outside it.
//
// A file whose shape the probe did not read is judged by none of it, and a
// person's import is never refused.

// wantedAspect is the shape c's items are kept in: the first of its keys
// the input names one for, "" when it names none (any kind but an episode,
// or an episode the cache did not hold).
func (p *planner) wantedAspect(c candidate) catalogv1alpha1.AspectRatio {
	for _, k := range c.keys {
		if want := p.in.WantedAspect[ItemKey(c.kind, k)]; want != "" {
			return want
		}
	}
	return ""
}

// aspectRejection refuses an incoming file of the wrong shape that would
// replace a file: releaseFault, since nothing but the release decides its
// shape.
func aspectRejection(rel string, want catalogv1alpha1.AspectRatio, c candidate, existing []catalogv1alpha1.MediaFile) Rejection {
	if len(existing) == 0 || c.f.Probe == nil || !catalogv1alpha1.WrongAspect(want, c.f.Probe.DisplayAspectMilli) {
		return Rejection{}
	}
	return releaseFaultRejection("%s: the file's picture is %s (%s) and the series is kept %s, so it replaces no file",
		rel, shapeName(catalogv1alpha1.AspectOf(c.f.Probe.DisplayAspectMilli)), ratioText(c.f.Probe.DisplayAspectMilli), shapeName(want))
}

// aspectRefit reports whether an incoming file of quality q, of the wanted
// shape, replaces existing because one of them is not, whatever their
// tiers: p must take q (quality.Profile.Lists), and the incoming file's
// shape must be known.
func aspectRefit(p quality.Profile, want catalogv1alpha1.AspectRatio, existing []catalogv1alpha1.MediaFile, c candidate, q commonv1.Quality) bool {
	if c.f.Probe == nil || !p.Lists(q) {
		return false
	}
	got := catalogv1alpha1.AspectOf(c.f.Probe.DisplayAspectMilli)
	if got == "" || catalogv1alpha1.WrongAspect(want, c.f.Probe.DisplayAspectMilli) {
		return false
	}
	for i := range existing {
		if existing[i].WrongAspect(want) {
			return true
		}
	}
	return false
}

// shapeName is a shape as a message names it.
func shapeName(a catalogv1alpha1.AspectRatio) string {
	if a == catalogv1alpha1.AspectRatioFullscreen {
		return "4:3"
	}
	return string(a)
}

// ratioText is a display aspect ratio in thousandths as "1.33:1".
func ratioText(milli int32) string {
	hundredths := (milli + 5) / 10
	return fmt.Sprintf("%d.%02d:1", hundredths/100, hundredths%100)
}
