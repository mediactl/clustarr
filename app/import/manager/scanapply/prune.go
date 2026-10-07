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

package scanapply

import (
	"context"
	"fmt"
	"path/filepath"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// missing deletes the record of a file gone from disk (P46, W20): the
// agent's two observations -- the MediaFile reconciler's FileMissing past
// mediafilespec.MissingGrace, and the file ENOENT -- checked again on a
// fresh read of the MediaFile the basis names, which must still be that
// object (its UID) at that path, then a delete with UID and resourceVersion
// preconditions. A record gone already, or changed since the read, is the
// next scan's to decide.
func (a *Applier) missing(ctx context.Context, obs schema.ScanObservation, t *tally) error {
	b := obs.Basis
	if b == nil || obs.Missing == nil {
		return fmt.Errorf("%w: a missing observation of %s without its basis or fact", errMalformed, obs.Path)
	}
	if !obs.Missing.ENOENT {
		// Unreadable is not gone.
		return nil
	}
	var mf catalogv1alpha1.MediaFile
	switch err := a.apiReader().Get(ctx, types.NamespacedName{Namespace: obs.Scan.Namespace, Name: b.Name}, &mf); {
	case apierrors.IsNotFound(err):
		return nil
	case err != nil:
		return fmt.Errorf("scanapply: get media file %s: %w", b.Name, err)
	}
	if string(mf.UID) != b.UID || filepath.Clean(mf.Spec.Path) != filepath.Clean(obs.Path) ||
		!mediafilespec.SeenMissing(&mf, a.now()) {
		return nil
	}
	if err := a.pace(ctx, mf.UID); err != nil {
		return err
	}
	err := a.Client.Delete(ctx, &mf, client.Preconditions{UID: &mf.UID, ResourceVersion: &mf.ResourceVersion})
	switch {
	case err == nil:
		t.FilesRemoved++
		logging.FromContext(ctx).Info("scanapply: removed the record of a file gone from disk",
			"mediaFile", mf.Name, "path", mf.Spec.Path)
	case apierrors.IsNotFound(err), apierrors.IsConflict(err):
		// Gone already, or changed since the read: the next scan decides.
	default:
		return fmt.Errorf("scanapply: remove media file %s: %w", mf.Name, err)
	}
	return nil
}
