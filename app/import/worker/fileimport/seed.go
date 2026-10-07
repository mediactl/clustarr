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

package fileimport

import (
	"context"
	"os"

	"k8s.io/apimachinery/pkg/types"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/probestore"
)

// seedProbe records the probe this import ran on the source as the probe
// record of the MediaFile it placed at dest: the same bytes (a hardlink, a
// move or a copy), summarised as a probe of dest reads them (AtPath), under
// the hash the MediaFile reconciler computes from dest's stat. It never fails
// the import: a seed that cannot be written costs one queued probe.
func (w *Worker) seedProbe(ctx context.Context, namespace, name string, uid types.UID, dest string, destInfo os.FileInfo, mi *commonv1.MediaInfo) {
	if w.Probes == nil || mi == nil || uid == "" || destInfo == nil {
		return
	}
	err := w.Probes.Seed(ctx, probestore.Seed{
		MediaFile:    schema.Ref{Namespace: namespace, Name: name, UID: string(uid)},
		Path:         dest,
		ProbeHash:    mediainfo.ProbeHash(dest, destInfo.Size(), destInfo.ModTime()),
		ProbeVersion: mediainfo.ProbeVersion,
		MediaInfo:    mediainfo.AtPath(mi, dest),
		Prober:       "importarr-fileimport",
	})
	if err != nil {
		logging.FromContext(ctx).Warn("fileimport: could not seed the probe record; catalogarr will queue a probe",
			"mediaFile", name, "error", err)
	}
}
