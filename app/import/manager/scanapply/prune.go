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

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// missing answers a scan that did not find a recorded file (P46, W20), on
// a fresh read of the MediaFile the basis names, which must still be that
// object (its UID) at that path:
//
//   - the MediaFile reconciler has had it FileMissing past
//     mediafilespec.MissingGrace: the two observations a removal wants. The
//     record is deleted with UID and resourceVersion preconditions.
//   - the reconciler has not marked it: it has not looked since the file
//     went, and nothing else would make it (a settled file has no timer
//     sooner than a day). The applier rings its doorbell, the
//     observed-fingerprint annotation (mediafilespec.ObservedMissing), and
//     the reconciler's own stat marks the file; the grace runs from there.
//   - marked, inside the grace: nothing. A share that blinks is not a file
//     gone.
//
// A record gone already, or changed since the read, is the next scan's to
// decide.
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
	if string(mf.UID) != b.UID || filepath.Clean(mf.Spec.Path) != filepath.Clean(obs.Path) {
		return nil
	}
	if !mediafilespec.SeenMissing(&mf, a.now()) {
		if _, marked := mediafilespec.MarkedMissing(&mf); marked {
			return nil
		}
		return a.handMissing(ctx, &mf, obs)
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

// handMissing tells catalog that a scan did not find mf's file: the one
// observed-fingerprint annotation apply handOver makes, under
// k8s.ManagerImport, carrying mediafilespec.ObservedMissing. The annotation
// wakes the file's pass, whose stat is the authority; a redelivered
// observation finds the annotation as it left it and writes nothing.
func (a *Applier) handMissing(ctx context.Context, mf *catalogv1alpha1.MediaFile, obs schema.ScanObservation) error {
	v := mediafilespec.ObservedMissing(obs.Missing.At)
	if mf.Annotations[mediafilespec.AnnotationObservedFingerprint] == v {
		return nil
	}
	if err := a.pace(ctx, mf.UID); err != nil {
		return err
	}
	ac := catalogac.MediaFile(mf.Name, mf.Namespace).
		WithAnnotations(map[string]string{mediafilespec.AnnotationObservedFingerprint: v})
	if _, err := k8s.Apply(ctx, a.Client, k8s.ManagerImport, ac); err != nil {
		return fmt.Errorf("scanapply: hand missing media file %s over to catalog: %w", mf.Name, err)
	}
	logging.FromContext(ctx).Info("scanapply: a recorded file is not on disk; handed to catalog",
		"mediaFile", mf.Name, "path", mf.Spec.Path)
	return nil
}
