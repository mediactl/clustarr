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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// prunePass reports the MediaFile of every file under the scanned path
// that is gone from disk (ADR-0019 §7.6, P46), as Sonarr's and Radarr's
// rescans remove a missing file's record: a folder renamed "Season 01" ->
// "Season 1" otherwise left a record at every old path for good (832 on the
// owner's library, 2026-10-01). A record is reported only on two
// observations -- the reconciler's FileMissing past
// mediafilespec.MissingGrace, and the
// file absent (ENOENT, nothing else) now -- never on a manual assignment,
// and never when the root folder itself is gone: an unmounted share is not
// a library whose files all went. The manager's scan applier re-reads the
// MediaFile and deletes it with UID and resourceVersion preconditions,
// counting it (FilesRemoved). A dry run reports nothing and counts what it
// would.
func (w *Worker) prunePass(ctx context.Context, st *scanState) error {
	if st.manual != nil {
		return nil
	}
	if info, err := os.Stat(st.root.Spec.Path); err != nil || !info.IsDir() {
		return nil
	}
	var files catalogv1alpha1.MediaFileList
	if err := w.Client.List(ctx, &files, client.InNamespace(st.scan.Namespace)); err != nil {
		return fmt.Errorf("rescan: list media files: %w", err)
	}
	now := w.now()
	for i := range files.Items {
		mf := &files.Items[i]
		if mf.Spec.Path == "" || !withinRoot(st.task.Path, mf.Spec.Path) || !seenMissing(mf, now) {
			continue
		}
		if _, err := os.Lstat(mf.Spec.Path); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if st.task.DryRun {
			st.progress.FilesRemoved++
			continue
		}
		if err := w.observe(ctx, st, schema.ScanObservation{
			Kind: schema.ScanObservationMissing, Path: mf.Spec.Path,
			Basis:   basisOf(mf),
			Missing: &schema.MissingFact{ENOENT: true, At: now},
		}); err != nil {
			return err
		}
		logging.FromContext(ctx).Info("library scan: reported the record of a file gone from disk",
			"mediaFile", mf.Name, "path", mf.Spec.Path)
	}
	return nil
}

// seenMissing reports whether the MediaFile reconciler has had mf's file
// missing for at least mediafilespec.MissingGrace (mediafilespec.SeenMissing, the rule the
// manager's scan applier decides by on a fresh read).
func seenMissing(mf *catalogv1alpha1.MediaFile, now time.Time) bool {
	return mediafilespec.SeenMissing(mf, now)
}
