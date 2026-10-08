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

package probe_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/app/remediation/remediationtest"
	"github.com/mediactl/clustarr/pkg/events"
)

// listRecorder records the field selector of every List it passes on, so a
// test can tell a lookup that selected one item's objects from one that
// listed the namespace and filtered in Go -- both return the same objects,
// so the result alone cannot.
type listRecorder struct {
	client.Reader
	mu        sync.Mutex
	selectors []string
}

func (r *listRecorder) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	lo := (&client.ListOptions{}).ApplyOptions(opts)
	sel := ""
	if lo.FieldSelector != nil {
		sel = lo.FieldSelector.String()
	}
	r.mu.Lock()
	r.selectors = append(r.selectors, sel)
	r.mu.Unlock()
	return r.Reader.List(ctx, list, opts...)
}

func (r *listRecorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.selectors
	r.selectors = nil
	return out
}

// TestLookupsSelectThroughTheManagerCachesIndex: production's reader is the
// manager's cache, and the lookups the MediaFile controller's became -- an
// item's files (mfindex.FilesOf, the naming wakes' FilesForMovie) and a
// RootFolder's movies' files (FilesForRootFolder) -- select through the
// field indexes remediation.RegisterIndexes adds: every List carries a
// selector, and each finds one item's files, not its neighbour's.
func TestLookupsSelectThroughTheManagerCachesIndex(t *testing.T) {
	var mgr ctrl.Manager
	e := remediationtest.StartManager(t, func(m ctrl.Manager, _ events.Bus) error {
		mgr = m
		return remediation.RegisterIndexes(context.Background(), m.GetFieldIndexer())
	})
	ctx := context.Background()
	const ns = "lookup-cache"
	mustNamespace(t, ctx, e.Admin, ns)
	mustQualityProfile(t, ctx, e.Admin, "qp-video")
	for _, rf := range []string{"movies", "other"} {
		require.NoError(t, e.Admin.Create(ctx, &catalogv1alpha1.RootFolder{
			ObjectMeta: metav1.ObjectMeta{Name: rf, Namespace: ns},
			Spec:       catalogv1alpha1.RootFolderSpec{Path: "/data/media/" + rf, Kind: catalogv1alpha1.RootFolderKindMovie},
		}))
	}
	mustMovie(t, ctx, e.Admin, ns, "film-a", "qp-video") // RootFolder "movies"
	mustMovie(t, ctx, e.Admin, ns, "film-b", "qp-video")
	other := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "film-c", Namespace: ns},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 1, QualityProfileRef: "qp-video", RootFolderRef: "other"},
	}
	require.NoError(t, e.Admin.Create(ctx, other))
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, m := range []string{"film-a", "film-b", "film-c"} {
		importCreates(t, e.Admin, ns, m+"-file", movieRef(m), "/data/media/movies/"+m+".mkv", 1000, at, bluray1080p)
	}

	rec := &listRecorder{Reader: mgr.GetClient()}
	names := func(files []catalogv1alpha1.MediaFile) []string {
		var out []string
		for _, f := range files {
			out = append(out, f.Name)
		}
		return out
	}
	nns := func(in []types.NamespacedName) []string {
		var out []string
		for _, n := range in {
			out = append(out, n.Name)
		}
		slices.Sort(out)
		return out
	}

	// The cache sees the objects a moment after the apiserver does.
	require.Eventually(t, func() bool {
		files, err := mfindex.FilesOf(ctx, rec, ns, commonv1.MediaKindMovie, "film-c")
		return err == nil && len(files) == 1
	}, 10*time.Second, 50*time.Millisecond, "the cache never served film-c's file")
	rec.take()

	files, err := mfindex.FilesOf(ctx, rec, ns, commonv1.MediaKindMovie, "film-a")
	require.NoError(t, err)
	assert.Equal(t, []string{"film-a-file"}, names(files), "film-b's file was read for film-a")
	assert.Equal(t, []string{mfindex.Item + "=movie/film-a"}, rec.take(),
		"the files were listed for the namespace, not selected for the item")

	var movie catalogv1alpha1.Movie
	require.NoError(t, mgr.GetClient().Get(ctx, client.ObjectKey{Namespace: ns, Name: "film-b"}, &movie))
	assert.Equal(t, []string{"film-b-file"}, nns(mediafile.FilesForMovie(ctx, rec, &movie)))
	assert.Equal(t, []string{mfindex.Item + "=movie/film-b"}, rec.take())

	var rf catalogv1alpha1.RootFolder
	require.NoError(t, mgr.GetClient().Get(ctx, client.ObjectKey{Namespace: ns, Name: "movies"}, &rf))
	assert.Equal(t, []string{"film-a-file", "film-b-file"}, nns(mediafile.FilesForRootFolder(ctx, rec, &rf)),
		"a RootFolder's files are its own movies', not another folder's")
	for _, sel := range rec.take() {
		assert.NotEmpty(t, sel, "a RootFolder's lookup listed a namespace without its index")
	}
}
