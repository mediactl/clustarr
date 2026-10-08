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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/import/importplan"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/app/import/worker/probe/probetest"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// errNoWrites is every write the import agent's client is asked for.
var errNoWrites = errors.New("the import agent writes no Kubernetes object")

// readOnlyClient is a fake client holding objs that fails every write and
// counts the writes it was asked for.
func readOnlyClient(t *testing.T, objs ...client.Object) (client.Client, func() []string) {
	t.Helper()
	var (
		mu     sync.Mutex
		writes []string
	)
	refuse := func(verb string, obj any) error {
		mu.Lock()
		defer mu.Unlock()
		writes = append(writes, fmt.Sprintf("%s %T", verb, obj))
		return errNoWrites
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(_ context.Context, _ client.WithWatch, o client.Object, _ ...client.CreateOption) error {
				return refuse("create", o)
			},
			Update: func(_ context.Context, _ client.WithWatch, o client.Object, _ ...client.UpdateOption) error {
				return refuse("update", o)
			},
			Patch: func(_ context.Context, _ client.WithWatch, o client.Object, _ client.Patch, _ ...client.PatchOption) error {
				return refuse("patch", o)
			},
			Apply: func(_ context.Context, _ client.WithWatch, o runtime.ApplyConfiguration, _ ...client.ApplyOption) error {
				return refuse("apply", o)
			},
			Delete: func(_ context.Context, _ client.WithWatch, o client.Object, _ ...client.DeleteOption) error {
				return refuse("delete", o)
			},
			DeleteAllOf: func(_ context.Context, _ client.WithWatch, o client.Object, _ ...client.DeleteAllOfOption) error {
				return refuse("deleteAllOf", o)
			},
			SubResourceCreate: func(_ context.Context, _ client.Client, sub string, o, _ client.Object, _ ...client.SubResourceCreateOption) error {
				return refuse(sub+" create", o)
			},
			SubResourceUpdate: func(_ context.Context, _ client.Client, sub string, o client.Object, _ ...client.SubResourceUpdateOption) error {
				return refuse(sub+" update", o)
			},
			SubResourcePatch: func(_ context.Context, _ client.Client, sub string, o client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
				return refuse(sub+" patch", o)
			},
			SubResourceApply: func(_ context.Context, _ client.Client, sub string, o runtime.ApplyConfiguration, _ ...client.SubResourceApplyOption) error {
				return refuse(sub+" apply", o)
			},
		}).Build()
	return c, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), writes...)
	}
}

// TestInspectAndExecuteWriteNoKubernetesObject (A3.8, W21-W24): the import
// agent's two handlers read the items, root folders, quality profiles and
// the MediaFiles a plan replaces, and write only their clustarr-imports
// records -- no MediaFile, no grab status, no AudioGraft. Against a client
// that fails every write, an upgrade over an existing file and an audio
// donor both inspect and execute to the end: the files are placed, the old
// one recycled, and the records say so.
func TestInspectAndExecuteWriteNoKubernetesObject(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	library, bin, downloads := filepath.Join(dir, "media", "movies"), filepath.Join(dir, "recycle"), filepath.Join(dir, "downloads")
	require.NoError(t, os.MkdirAll(library, 0o755))
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

	qp := testQualityProfile("hd")
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: "media"},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: library, Kind: catalogv1alpha1.RootFolderKindMovie, RecycleBin: catalogv1alpha1.RecycleBin{Path: bin},
		},
	}
	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: "media", UID: "heat-uid"},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: qp.Name, RootFolderRef: rf.Name},
		Status: catalogv1alpha1.MovieStatus{Metadata: &catalogv1alpha1.MovieMetadata{
			Title: "Heat", Year: 1995, OriginalLanguage: "ja",
		}},
	}
	oldPath := filepath.Join(library, "Heat (1995)", "Heat.1995.720p.HDTV.x264-OLD.mkv")
	mustWriteSparseFile(t, oldPath, sampleFloor)
	hdtv, ok := quality.Lookup("video", "HDTV-720p")
	require.True(t, ok)
	old := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "heat-old", Namespace: "media", UID: "heat-old-uid"},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}, Path: oldPath, Quality: hdtv.Quality,
		},
	}
	c, writes := readOnlyClient(t, qp, rf, movie, old)
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(old), old))

	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	prober := probetest.ProberFunc(func(context.Context, string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
		return &commonv1.MediaInfo{
			VideoCodec: "h264", Width: 1920, Height: 1080,
			Audio: []commonv1.AudioStream{{Language: "jpn"}, {Language: "eng"}},
		}, nil, nil
	})
	w := fileimport.NewWorker(c, bus, prober)
	w.Clock = func() time.Time { return now }
	w.ReduceDonor = func(_ context.Context, src, _ string, _ []string) (string, error) {
		mka := src[:len(src)-len(filepath.Ext(src))] + ".mka"
		if err := os.Rename(src, mka); err != nil {
			return "", err
		}
		return mka, nil
	}
	prof, errs := quality.FromCRD(qp, catalogue.LoadedCatalogue())
	require.Empty(t, errs)
	owner := schema.ItemRef{Kind: "Movie", Ref: schema.Ref{Namespace: "media", Name: movie.Name, UID: string(movie.UID)}}
	run := func(uid, title string, purpose commonv1.DownloadPurpose, donor *importplan.DonorItem) *importRun {
		root := filepath.Join(downloads, uid)
		mustWriteSparseFile(t, filepath.Join(root, title+".mkv"), sampleFloor)
		return &importRun{t: t, w: w, bus: bus, existing: c, in: importplan.Input{
			Owner: owner, OwnerKind: commonv1.MediaKindMovie,
			Entry: catalogv1alpha1.DownloadEntry{
				ID: "dl-" + uid, UID: uid, Purpose: purpose,
				Release:   catalogv1alpha1.DownloadRelease{Title: title, Protocol: commonv1.ProtocolTorrent},
				GrabbedBy: commonv1.GrabSourceSearch, GrabbedAt: metav1.NewTime(now.Add(-time.Hour)),
				Phase: commonv1.DownloadPhaseCompleted,
			},
			Transfer: &schema.TransferRecord{ContentRoot: root}, Targets: []schema.ItemRef{owner},
			ProfileRef: qp.Name, Profile: &prof, OriginalLanguage: "ja", Donor: donor, Now: now,
		}}
	}

	// An upgrade over the movie's existing file.
	up := run("upgrade-entry", "Heat.1995.1080p.BluRay.x264-GRP", "", nil)
	up.decide()
	up.deliver() // the inspect
	insp := up.record(schema.ImportSubInspect)
	require.NotNil(t, insp)
	require.NotNil(t, insp.Inspect)
	require.Len(t, insp.Inspect.Files, 1, "%+v", insp.Inspect)
	up.decide()
	task := up.executeTask()
	require.Len(t, task.Plan.Replaces, 1)
	up.deliver() // the execute
	x := up.record(schema.ImportSubExecute)
	require.NotNil(t, x)
	require.NotNil(t, x.Execute, x.Failure)
	assert.Empty(t, x.Execute.Refused, x.Failure)
	require.Len(t, x.Execute.Placed, 1)
	assert.FileExists(t, x.Execute.Placed[0].Dest)
	assert.Equal(t, []string{oldPath}, x.Execute.Recycled)
	dec := up.decide()
	assert.Equal(t, lifecycle.ImportImported, dec.Verdict, "the manager materialises what the agent placed")
	require.Len(t, dec.Materialise, 2, "one MediaFile applied, the replaced one deleted -- by the manager")

	// An audio donor: placed, reduced, and recorded -- no AudioGraft.
	donor := run("donor-entry", "Heat.1995.1080p.BluRay.DUAL-GRP", commonv1.DownloadPurposeAudioDonor,
		&importplan.DonorItem{Item: owner, Missing: []string{"en"}, Original: "ja"})
	donor.decide()
	donor.deliver()
	donor.decide()
	donor.executeTask()
	donor.deliver()
	dx := donor.record(schema.ImportSubExecute)
	require.NotNil(t, dx)
	require.NotNil(t, dx.Execute, dx.Failure)
	assert.Empty(t, dx.Execute.Refused, dx.Failure)
	assert.FileExists(t, dx.Execute.DonorPath)
	assert.Equal(t, ".mka", filepath.Ext(dx.Execute.DonorPath))

	assert.Empty(t, writes(), "the import agent asked for Kubernetes writes")
}
