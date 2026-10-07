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
	"time"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
)

// renderMain is the one main-resource apply under catalogarr (§3.10): the
// seven catalog.clustarr.io labels from MirrorLabels over the draft's
// mediaInfo, and the spec takeover after a swap (spec.original false, or one
// this pass incorporates) or for a grafted original (the flat
// status.graftTag, never the graft block). It reports false when the cached
// object already carries all of it, and before any probe (as before the
// loop, labels wait for one).
//
// A takeover the cached object already carries is not re-sent: catalogarr
// already owns those leaves, and the next apply that is sent re-sends them,
// so nothing is released. A main apply that is sent carries every takeover
// field.
func renderMain(mf *catalogv1alpha1.MediaFile, draft *catalogv1alpha1.MediaFileStatus, intent *MainIntent) (*catalogac.MediaFileApplyConfiguration, bool) {
	if draft.MediaInfo == nil {
		return nil, false
	}
	original := mf.Spec.Original == nil || *mf.Spec.Original
	path, size, mod := mf.Spec.Path, mf.Spec.SizeBytes, mf.Spec.ModTime
	if intent != nil {
		path, size, mod = intent.Path, intent.SizeBytes, intent.ModTime
		if intent.Swap {
			original = false
		}
	}
	labels := mediafile.MirrorLabels(mf.Spec.MediaRef.Kind, mf.Spec.Quality, draft.MediaInfo, original)
	ac := catalogac.MediaFile(mf.Name, mf.Namespace).WithLabels(labels)
	takeover := !original || draft.GraftTag != ""
	switch {
	case !original:
		ac.WithSpec(catalogac.MediaFileSpec().WithPath(path).WithSizeBytes(size).WithModTime(mod).WithOriginal(false))
	case draft.GraftTag != "":
		ac.WithSpec(catalogac.MediaFileSpec().WithPath(path).WithSizeBytes(size).WithModTime(mod))
	}
	current := map[string]string{}
	for _, k := range mediafile.LoopLabelKeys {
		if v, ok := mf.Labels[k]; ok {
			current[k] = v
		}
	}
	same := maps.Equal(current, labels)
	if takeover {
		same = same && path == mf.Spec.Path && size == mf.Spec.SizeBytes &&
			mod.UTC().Truncate(time.Second).Equal(mf.Spec.ModTime.UTC().Truncate(time.Second)) && (original || (mf.Spec.Original != nil && !*mf.Spec.Original))
	}
	return ac, !same
}
