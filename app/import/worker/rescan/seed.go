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

package rescan

import (
	"context"
	"os"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/probestore"
)

// seedProbe records the probe this scan ran on path as applied's probe
// record. It never fails the scan: a seed that cannot be written costs one
// queued probe.
func (w *Worker) seedProbe(ctx context.Context, namespace string, applied appliedFile, path string, info os.FileInfo, mi *commonv1.MediaInfo) {
	if w.Probes == nil || mi == nil || applied.UID == "" || info == nil {
		return
	}
	err := w.Probes.Seed(ctx, probestore.Seed{
		MediaFile:    schema.Ref{Namespace: namespace, Name: applied.Name, UID: string(applied.UID)},
		Path:         path,
		ProbeHash:    mediainfo.ProbeHash(path, info.Size(), info.ModTime()),
		ProbeVersion: mediainfo.ProbeVersion,
		MediaInfo:    mediainfo.AtPath(mi, path),
		Prober:       "importarr-scan",
	})
	if err != nil {
		logging.FromContext(ctx).Warn("rescan: could not seed the probe record; catalogarr will queue a probe",
			"mediaFile", applied.Name, "error", err)
	}
}
