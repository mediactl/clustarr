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

package fileimport_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/importarr/worker/fileimport"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestHandleImportsAFileAndOwnsOnlySpec is the phase gate: a completed
// Download's one file is hard-linked into the library, a MediaFile records
// it, and Download.status.import reports the outcome -- and the two writes
// land under the two different field managers task D2-7 settled on.
func TestHandleImportsAFileAndOwnsOnlySpec(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-happy")

	contentRoot := dataDir(t, "scratch")
	srcPath := filepath.Join(contentRoot, "The.Matrix.1999.1080p.BluRay.x264-SPARKS.mkv")
	mustWriteSparseFile(t, srcPath, sampleFloor)

	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName}
	dl := f.createDownload(t, "matrix-dl", contentRoot, target)

	msg := newImportTaskMessage(t, f.ns, dl.Name, "")
	require.NoError(t, f.worker.Handle(ctx, msg))

	// The Download side: status.import reports Imported, one file, under
	// k8s.ManagerImportarr -- the bare controller-manager name task D2-7
	// settled on (see k8s.ManagerImportarr's doc comment), not the
	// FieldManager this same worker uses for MediaFile.
	var gotDL downloadv1alpha1.Download
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: dl.Name}, &gotDL))
	require.NotNil(t, gotDL.Status.Import)
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, gotDL.Status.Import.State)
	require.Len(t, gotDL.Status.Import.Imported, 1)
	mfName := gotDL.Status.Import.Imported[0].MediaFileRef
	require.NotEmpty(t, mfName)
	require.Contains(t, gotDL.Status.Import.Imported[0].DestPath, f.mediaRoot)

	require.Equal(t, string(k8s.ManagerImportarr),
		managerFor(t, gotDL.ManagedFields, "status", "status.import"),
		"status.import must be owned by k8s.ManagerImportarr, not any other manager")

	// The MediaFile side: spec only, under the worker's FieldManager, and no
	// writer has ever touched status.
	var gotMF catalogv1alpha1.MediaFile
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: mfName}, &gotMF))
	require.Equal(t, gotDL.Status.Import.Imported[0].DestPath, gotMF.Spec.Path)
	require.Equal(t, sampleFloor, int(gotMF.Spec.SizeBytes))
	require.Equal(t, commonv1.MediaKindMovie, gotMF.Spec.MediaRef.Kind)
	require.Equal(t, f.movieName, gotMF.Spec.MediaRef.Name)
	require.NotEmpty(t, gotMF.Spec.ProfileHash, "the profile the file was scored against must be recorded")
	require.NotNil(t, gotMF.Spec.Original)
	require.True(t, *gotMF.Spec.Original)
	require.NotNil(t, gotMF.Spec.ImportedFrom)
	require.Equal(t, dl.Name, gotMF.Spec.ImportedFrom.DownloadRef)

	specManager := managerFor(t, gotMF.ManagedFields, "", "spec")
	require.Equal(t, "importarr-worker", specManager, "MediaFileSpec must be owned by k8s.ManagerImportarrWorker")
	statusManager := managerFor(t, gotMF.ManagedFields, "status", "status.conditions")
	require.Empty(t, statusManager, "nothing in this package may ever claim any part of MediaFileStatus")

	// The imported file must actually exist at the recorded destination.
	info, err := os.Stat(gotMF.Spec.Path)
	require.NoError(t, err)
	require.Equal(t, sampleFloor, int(info.Size()))
}

// TestHandleRedeliveryAfterImportIsANoOp proves the dedup fingerprint (R8,
// events.BucketDedup) makes a second delivery of the same ImportTask a
// no-op: it must not create a second MediaFile or otherwise reprocess the
// files.
func TestHandleRedeliveryAfterImportIsANoOp(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-redeliver")

	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, "The.Matrix.1999.1080p.BluRay.x264-SPARKS.mkv"), sampleFloor)

	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName}
	dl := f.createDownload(t, "matrix-dl2", contentRoot, target)

	// The UID a real producer would carry is the apiserver-assigned one,
	// read back off the object -- a mismatched UID makes Handle discard the
	// task as "download was replaced" (the redelivery-after-recreate guard),
	// which is a different scenario from the one this test exercises.
	var created downloadv1alpha1.Download
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: dl.Name}, &created))
	uid := string(created.UID)
	require.NotEmpty(t, uid)

	msg := newImportTaskMessage(t, f.ns, dl.Name, uid)
	require.NoError(t, f.worker.Handle(ctx, msg))

	var mfList catalogv1alpha1.MediaFileList
	require.NoError(t, f.api.List(ctx, &mfList, client.InNamespace(f.ns)))
	require.Len(t, mfList.Items, 1)

	// A redelivery: same envelope, same UID, a fresh attempt. The dedup
	// fingerprint short-circuits it before any file is touched again.
	require.NoError(t, f.worker.Handle(ctx, msg))

	require.NoError(t, f.api.List(ctx, &mfList, client.InNamespace(f.ns)))
	require.Len(t, mfList.Items, 1, "a redelivered ImportTask must not create a second MediaFile")

	_, err := f.bus.KV(events.BucketDedup).Get(ctx, fileimport.DedupKey(uid))
	require.NoError(t, err, "a dedup fingerprint must exist for the imported download")
}
