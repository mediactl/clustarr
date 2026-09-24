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

package rescan_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/rescan"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// importedFile plants a scanned movie file and a MediaFile recording it as
// the file-import worker would, under rescan.FieldManager, and returns the
// MediaFile's name, its path and the file's stat.
func (f *fixture) importedFile(t *testing.T, ctx context.Context) (name, path string, info os.FileInfo) {
	t.Helper()
	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat-949", Namespace: f.ns},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "hd-bluray-web", RootFolderRef: f.rf.Name},
	}
	require.NoError(t, f.c.Create(ctx, movie))

	path = filepath.Join(f.root, "Heat (1995) [tmdbid-949]", "Heat (1995) [tmdbid-949] - Bluray-1080p.mkv")
	mustWriteFile(t, path, sampleFloor)
	info, err := os.Stat(path)
	require.NoError(t, err)

	name = k8s.ChildName(movie.Name, "mediafile", path)
	_, err = k8s.Apply(ctx, f.c, rescan.FieldManager, catalogac.MediaFile(name, f.ns).WithSpec(
		catalogac.MediaFileSpec().
			WithMediaRef(commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}).
			WithPath(path).
			WithSizeBytes(info.Size()).
			WithModTime(metav1.NewTime(info.ModTime())).
			WithQuality(commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: 1080}).
			WithOriginal(true)))
	require.NoError(t, err)
	return name, path, info
}

// takeOver is catalogarr incorporating a transcode swap: it applies
// spec.sizeBytes, spec.modTime and spec.original=false under its own
// manager, exactly as app/catalog/controller/mediafile does.
func takeOver(ctx context.Context, c client.Client, ns, name string, size int64, mod time.Time) error {
	_, err := k8s.Apply(ctx, c, k8s.ManagerCatalogarr, catalogac.MediaFile(name, ns).WithSpec(
		catalogac.MediaFileSpec().WithSizeBytes(size).WithModTime(metav1.NewTime(mod)).WithOriginal(false)))
	return err
}

func (f *fixture) waitOriginal(t *testing.T, ctx context.Context, name string, original bool) {
	t.Helper()
	waitFor(t, 10*time.Second, func() bool {
		var mf catalogv1alpha1.MediaFile
		return f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: name}, &mf) == nil &&
			mf.Spec.Original != nil && *mf.Spec.Original == original
	})
}

// The X7a/X5a contract, first half: once catalogarr has taken a transcoded
// file over, a rescan writes none of the fields it owns. A file whose bytes
// changed on disk since is handed to catalogarr through ONE annotation,
// under k8s.ManagerImportarr -- which owns nothing else on a MediaFile, so
// the apply releases nothing -- and a later scan that finds the same bytes
// does not write it again.
func TestHandleHandsAChangedPostTranscodeFileToCatalogarr(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-handover", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
	name, _, info := f.importedFile(t, ctx)

	stale := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, takeOver(ctx, f.c, f.ns, name, 999, stale))
	f.waitOriginal(t, ctx, name, false)

	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))

	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	assert.Equal(t, int64(1), got.HandedOver)
	assert.Equal(t, int64(1), got.FilesMatched, "a handed-over file is still an attributed one")
	assert.Zero(t, got.FilesSkipped)

	want := rescan.ObservedFingerprint(info.Size(), info.ModTime())
	var after catalogv1alpha1.MediaFile
	waitFor(t, 10*time.Second, func() bool {
		return f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: name}, &after) == nil &&
			after.Annotations[rescan.AnnotationObservedFingerprint] == want
	})
	assert.Equal(t, int64(999), after.Spec.SizeBytes, "catalogarr's size is not overwritten")
	assert.True(t, after.Spec.ModTime.Equal(&metav1.Time{Time: stale}), "nor its mtime")
	require.NotNil(t, after.Spec.Original)
	assert.False(t, *after.Spec.Original, "nor its original flag")

	assert.Equal(t, string(k8s.ManagerImportarr), managerForParts(t, after.ManagedFields, "",
		"metadata", "annotations", rescan.AnnotationObservedFingerprint))
	for _, leaf := range []string{"spec.sizeBytes", "spec.modTime", "spec.original"} {
		assert.Equal(t, string(k8s.ManagerCatalogarr), managerFor(t, after.ManagedFields, "", leaf), leaf)
	}
	for _, leaf := range []string{"spec.path", "spec.mediaRef", "spec.quality"} {
		assert.Equal(t, string(rescan.FieldManager), managerFor(t, after.ManagedFields, "", leaf),
			"%s: the annotation's apply released nothing importarr-worker owns", leaf)
	}

	// The next scan finds the same bytes: nothing is written again.
	next, msg := f.nextScan(t, ctx, "tick-2")
	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, msg))
	assert.Equal(t, int64(1), readProgress(t, ctx, f.bus, string(next.UID)).HandedOver)
	var again catalogv1alpha1.MediaFile
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: name}, &again))
	assert.Equal(t, after.ResourceVersion, again.ResourceVersion, "an unchanged observation is not re-applied")
}

// The contract's other half: a post-transcode file whose bytes are what
// catalogarr recorded is left alone and counted as a transcoded skip.
func TestHandleLeavesAnUnchangedPostTranscodeFileAlone(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-transcoded", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
	name, _, info := f.importedFile(t, ctx)
	require.NoError(t, takeOver(ctx, f.c, f.ns, name, info.Size(), info.ModTime()))
	f.waitOriginal(t, ctx, name, false)

	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))

	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	assert.Equal(t, int64(1), got.Transcoded)
	assert.Equal(t, int64(1), got.FilesSkipped)
	assert.Zero(t, got.FilesMatched)
	assert.Zero(t, got.HandedOver)

	var after catalogv1alpha1.MediaFile
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: name}, &after))
	assert.NotContains(t, after.Annotations, rescan.AnnotationObservedFingerprint)
}

// takeoverOnApply is the cluster between a rescan's read of a MediaFile and
// its apply: the first time the worker applies the MediaFile, catalogarr's
// transcode takeover lands first.
type takeoverOnApply struct {
	client.Client
	ns, name string
	size     int64
	mod      time.Time
	fired    atomic.Bool
}

func (c *takeoverOnApply) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
	if ac, ok := obj.(*catalogac.MediaFileApplyConfiguration); ok && ac.Spec != nil && ac.Name != nil &&
		*ac.Name == c.name && c.fired.CompareAndSwap(false, true) {
		if err := takeOver(ctx, c.Client, c.ns, c.name, c.size, c.mod); err != nil {
			return err
		}
	}
	return c.Client.Apply(ctx, obj, opts...)
}

// The swap-versus-rescan race: a transcode takeover that lands between the
// walk reading a MediaFile (still original) and applying it. Without the
// resourceVersion precondition the forced apply put spec.original back to
// true and the pre-transcode size back; with it the apiserver refuses the
// stale apply, and the walk re-reads and leaves the swapped file to
// catalogarr.
func TestHandleNeverRevertsATakeoverThatLandsMidScan(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-race", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
	name, _, info := f.importedFile(t, ctx)
	f.waitOriginal(t, ctx, name, true)

	swapped := info.ModTime().Add(time.Hour)
	racy := &takeoverOnApply{Client: f.c, ns: f.ns, name: name, size: 12345, mod: swapped}
	require.NoError(t, rescan.NewWorker(racy, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))
	require.True(t, racy.fired.Load(), "setup: the takeover raced the walk's apply")

	var after catalogv1alpha1.MediaFile
	require.NoError(t, f.api(t).Get(ctx, types.NamespacedName{Namespace: f.ns, Name: name}, &after))
	require.NotNil(t, after.Spec.Original)
	assert.False(t, *after.Spec.Original, "the takeover's original=false stands")
	assert.Equal(t, int64(12345), after.Spec.SizeBytes, "and its size")
	assert.Equal(t, string(k8s.ManagerCatalogarr), managerFor(t, after.ManagedFields, "", "spec.original"))

	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	assert.Equal(t, int64(1), got.HandedOver+got.Deferred,
		"the walk re-decided the file as a changed post-transcode file (or left it to the next scan)")
}
