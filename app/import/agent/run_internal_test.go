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

package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/fsops"
)

// TestWorkersGetTheSampleSizeFloor holds the second link of the sample size
// floor's wiring: Options.SampleMaxBytes reaches BOTH workers setupWorkers
// subscribes -- the rescan (which lists a suspected sample as unmatched) and
// the completed-download import (which rejects one). cmd/clustarr's
// TestImportarrSampleMaxBytesReachesTheOptions holds the first, the flag to
// Options.
//
// The non-default values carry the proof. Each worker's NewWorker already
// sets fsops.DefaultSampleMaxBytes, so a setupWorkers that forgot the
// assignment would still pass the default row; only a floor NewWorker would
// not choose on its own -- 1 MiB, or 0 for "disabled" -- shows the option
// reached the worker.
func TestWorkersGetTheSampleSizeFloor(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    Options
		want int64
	}{
		{name: "DefaultOptions", o: DefaultOptions(), want: fsops.DefaultSampleMaxBytes},
		{name: "a lower floor", o: Options{SampleMaxBytes: 1 << 20}, want: 1 << 20},
		{name: "disabled", o: Options{SampleMaxBytes: 0}, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, newScanWorker(nil, nil, nil, tc.o).SampleMaxBytes,
				"the rescan worker (work.importarr.scan) did not get Options.SampleMaxBytes")
			require.Equal(t, tc.want, newImportWorker(nil, nil, nil, tc.o).SampleMaxBytes,
				"the file-import worker (work.importarr.fileimport) did not get Options.SampleMaxBytes")
		})
	}
}

// The movie import gate reads a movie's existing files through the API
// reader (fileimport.Worker.APIReader); a worker built without it would
// quietly fall back to the cache.
func TestImportWorkerGetsTheAPIReader(t *testing.T) {
	api := fake.NewClientBuilder().Build()
	require.Same(t, api, newImportWorker(nil, api, nil, DefaultOptions()).APIReader)
}

// A scan's rename pass re-reads each MediaFile through the API reader
// (rescan.Worker.APIReader) immediately before it moves the file; a worker
// built without it would quietly fall back to the cache.
func TestScanWorkerGetsTheAPIReader(t *testing.T) {
	api := fake.NewClientBuilder().Build()
	require.Same(t, api, newScanWorker(nil, api, nil, DefaultOptions()).APIReader)
}

func TestDefaultOptionsTurnTheSampleSizeFloorOn(t *testing.T) {
	require.Equal(t, fsops.DefaultSampleMaxBytes, DefaultOptions().SampleMaxBytes)
}

// TestImportListsGetTheirBaseURLs holds the second link of the
// --trakt-base-url/--plex-base-url wiring: Options reach the list worker
// (watchlist syncs and token refresh) and the ImportList controller (the
// device-code flow), and the Trakt host is the same one in both, so a
// device authorization and the syncs it authorizes name one host.
// cmd/clustarr's TestImportarrListBaseURLsReachTheOptions holds the first,
// the flag to Options. Unset is the providers' own public default.
func TestImportListsGetTheirBaseURLs(t *testing.T) {
	o := Options{TraktBaseURL: "http://trakt.fixture:8080", PlexBaseURL: "http://plex.fixture:8080"}

	w := newListWorker(nil, nil, o)
	require.Equal(t, o.TraktBaseURL, w.TraktBaseURL, "the list worker did not get Options.TraktBaseURL")
	require.Equal(t, o.PlexBaseURL, w.PlexBaseURL, "the list worker did not get Options.PlexBaseURL")

	r := newImportListReconciler(nil, nil, o)
	require.Equal(t, o.TraktBaseURL, r.TraktBaseURL,
		"the ImportList controller's device flow did not get Options.TraktBaseURL")

	d := newListWorker(nil, nil, DefaultOptions())
	require.Empty(t, d.TraktBaseURL, "an unset Trakt base URL must leave the provider's own default")
	require.Empty(t, d.PlexBaseURL, "an unset Plex base URL must leave the provider's own default")
}

// The controller role's MediaFile transform chains the manager's
// DefaultTransform rather than replacing it, so whatever
// pkg/k8s.ManagerOptions' default does -- stripping managedFields today --
// still reaches MediaFiles; with no default it strips managedFields itself.
func TestWithoutMediaInfoChainsTheDefaultTransform(t *testing.T) {
	newMediaFile := func() *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{
				Name:          "heat",
				ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "catalogarr"}},
			},
			Status: catalogv1alpha1.MediaFileStatus{
				MediaInfo: &commonv1.MediaInfo{Width: 1920, Height: 1080},
				ProbeHash: "hash",
			},
		}
	}

	t.Run("a default transform runs first", func(t *testing.T) {
		labelling := func(obj any) (any, error) {
			obj.(*catalogv1alpha1.MediaFile).Labels = map[string]string{"transformed": "by-default"}
			return obj, nil
		}
		want := newMediaFile()
		want.Labels = map[string]string{"transformed": "by-default"}
		want.Status.MediaInfo = nil

		got, err := withoutMediaInfo(labelling)(newMediaFile())
		require.NoError(t, err)
		require.Equal(t, want, got, "the default's change kept, managedFields left to it, mediaInfo dropped")
	})

	t.Run("no default strips managedFields", func(t *testing.T) {
		want := newMediaFile()
		want.ManagedFields = nil
		want.Status.MediaInfo = nil

		got, err := withoutMediaInfo(nil)(newMediaFile())
		require.NoError(t, err)
		require.Equal(t, want, got)
	})
}
