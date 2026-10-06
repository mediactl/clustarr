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

package mediafile_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	subtitleac "github.com/mediactl/clustarr/api/applyconfiguration/subtitle/subtitle/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// listRecorder records the field selector of every List it passes on, so a
// test can tell a lookup that asked for one MediaFile's objects from one
// that listed the namespace and filtered in Go -- both return the same
// objects, so the result alone cannot.
type listRecorder struct {
	client.Client
	mu        sync.Mutex
	selectors map[string]string // list type -> field selector, "" for none
}

func (r *listRecorder) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	lo := (&client.ListOptions{}).ApplyOptions(opts)
	sel := ""
	if lo.FieldSelector != nil {
		sel = lo.FieldSelector.String()
	}
	r.mu.Lock()
	if r.selectors == nil {
		r.selectors = map[string]string{}
	}
	switch list.(type) {
	case *transcodev1alpha1.TranscodeJobList:
		r.selectors["TranscodeJob"] = sel
	case *subtitlev1alpha1.SubtitleRequestList:
		r.selectors["SubtitleRequest"] = sel
	}
	r.mu.Unlock()
	return r.Client.List(ctx, list, opts...)
}

func (r *listRecorder) selector(kind string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.selectors[kind]
}

// seedLookupFixtures creates, for each of two MediaFiles, one TranscodeJob
// and one SubtitleRequest carrying a downloaded sidecar in its own
// language, so a lookup that returns the other file's object is visible.
func seedLookupFixtures(t *testing.T, ctx context.Context, c client.Client, ns string) {
	t.Helper()
	mustNamespace(t, ctx, c, ns)
	for name, lang := range map[string]string{"film-a": "en", "film-b": "fr"} {
		require.NoError(t, c.Create(ctx, &transcodev1alpha1.TranscodeJob{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-job", Namespace: ns},
			Spec:       transcodev1alpha1.TranscodeJobSpec{MediaFileRef: name, ProfileRef: "hevc", SourcePath: "/data/" + name + ".mkv"},
		}))
		require.NoError(t, c.Create(ctx, &subtitlev1alpha1.SubtitleRequest{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec:       subtitlev1alpha1.SubtitleRequestSpec{MediaFileRef: name},
		}))
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCaptionarrWorker,
			subtitleac.SubtitleRequest(name, ns).WithStatus(subtitleac.SubtitleRequestStatus().WithItems(
				subtitleac.SubtitleItem().WithLangKey(lang).WithState(subtitlev1alpha1.SubtitleItemDownloaded).
					WithPath(name+"."+lang+".srt"))))
		require.NoError(t, err)
	}
}

func lookupMediaFile(ns, name string) *catalogv1alpha1.MediaFile {
	return &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       catalogv1alpha1.MediaFileSpec{Path: "/data/" + name + ".mkv"},
	}
}

// TestScanSidecarsSelectsOnTheServerThroughARawClient: SubtitleRequest
// declares spec.mediaFileRef a selectable field, so a raw client's
// MatchingFields reaches the apiserver as a fieldSelector it accepts, and
// one MediaFile's reconcile reads its own request rather than the whole
// namespace's (one reconcile per file used to list every request).
func TestScanSidecarsSelectsOnTheServerThroughARawClient(t *testing.T) {
	c, _ := startEnv(t)
	ctx := t.Context()
	const ns = "lookup-raw"
	seedLookupFixtures(t, ctx, c, ns)

	rec := &listRecorder{Client: c}
	r := &mediafile.Reconciler{Client: rec}
	sidecars, matched, err := mediafile.ScanSidecars(r, ctx, lookupMediaFile(ns, "film-a"))
	require.NoError(t, err)
	require.True(t, matched)
	require.Len(t, sidecars, 1)
	assert.Equal(t, "en", sidecars[0].Language, "film-b's request was read for film-a")
	assert.Equal(t, mediafile.MediaFileRefIndex+"=film-a", rec.selector("SubtitleRequest"),
		"the SubtitleRequests were listed for the namespace, not selected for the file")
}

// TestTranscodeJobHasNoSelectableMediaFileRefYet records why the raw-client
// envtests reconcile through rawClient's TranscodeJob shim: api/transcode
// does not yet declare spec.mediaFileRef selectable, so the apiserver
// refuses the selector production's cached client answers from its index.
// When TranscodeJob gains the selectable field SubtitleRequest has, this
// fails: delete it and the shim.
func TestTranscodeJobHasNoSelectableMediaFileRefYet(t *testing.T) {
	c, _ := startEnv(t)
	raw := c.(rawClient).WithWatch
	var list transcodev1alpha1.TranscodeJobList
	err := raw.List(t.Context(), &list, client.InNamespace("default"),
		client.MatchingFields{mediafile.MediaFileRefIndex: "film-a"})
	require.Error(t, err, "TranscodeJob is selectable on spec.mediaFileRef now: remove rawClient's shim and this test")
}

// TestLookupsSelectThroughTheManagerCachesIndex: production's client is
// the manager's, whose reads are served by the cache, and both lookups
// select through the field indexes RegisterIndexes adds.
func TestLookupsSelectThroughTheManagerCachesIndex(t *testing.T) {
	c, cfg := startEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	const ns = "lookup-cache"
	seedLookupFixtures(t, ctx, c, ns)

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)
	require.NoError(t, mediafile.RegisterIndexes(ctx, mgr.GetFieldIndexer()))
	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))

	rec := &listRecorder{Client: mgr.GetClient()}
	r := &mediafile.Reconciler{Client: rec}
	mf := lookupMediaFile(ns, "film-a")

	jobs, err := mediafile.TranscodeJobsOf(r, ctx, mf)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, "film-a-job", jobs[0].Name)
	assert.Equal(t, mediafile.MediaFileRefIndex+"=film-a", rec.selector("TranscodeJob"),
		"the TranscodeJobs were listed for the namespace, not selected for the file")

	sidecars, matched, err := mediafile.ScanSidecars(r, ctx, mf)
	require.NoError(t, err)
	require.True(t, matched)
	require.Len(t, sidecars, 1)
	assert.Equal(t, "en", sidecars[0].Language)
	assert.Equal(t, mediafile.MediaFileRefIndex+"=film-a", rec.selector("SubtitleRequest"))
}

// rawClient is startEnv's direct, uncached client, plus one shim: a
// TranscodeJob List selecting on spec.mediaFileRef is answered by listing
// the namespace and filtering here, because the apiserver refuses that
// selector until api/transcode declares it selectable (see
// TestTranscodeJobHasNoSelectableMediaFileRefYet). It stands in for the
// selectable field, in tests only: production reconciles through the
// manager's cache, which answers it from the index RegisterIndexes adds.
// Every other call, a SubtitleRequest List included, reaches the apiserver
// unchanged.
type rawClient struct{ client.WithWatch }

func (c rawClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if grafts, ok := list.(*transcodev1alpha1.AudioGraftList); ok {
		lo := (&client.ListOptions{}).ApplyOptions(opts)
		ref, indexed := "", false
		if lo.FieldSelector != nil {
			ref, indexed = lo.FieldSelector.RequiresExactMatch(mediafile.AudioGraftMediaFileRefIndex)
		}
		lo.FieldSelector = nil
		if err := c.WithWatch.List(ctx, grafts, lo); err != nil {
			return err
		}
		if indexed {
			kept := grafts.Items[:0]
			for i := range grafts.Items {
				if grafts.Items[i].Status.MediaFileRef == ref {
					kept = append(kept, grafts.Items[i])
				}
			}
			grafts.Items = kept
		}
		return nil
	}
	jobs, ok := list.(*transcodev1alpha1.TranscodeJobList)
	if !ok {
		return c.WithWatch.List(ctx, list, opts...)
	}
	lo := (&client.ListOptions{}).ApplyOptions(opts)
	if lo.FieldSelector == nil {
		return c.WithWatch.List(ctx, list, opts...)
	}
	ref, ok := lo.FieldSelector.RequiresExactMatch(mediafile.MediaFileRefIndex)
	if !ok {
		return c.WithWatch.List(ctx, list, opts...)
	}
	lo.FieldSelector = nil
	if err := c.WithWatch.List(ctx, jobs, lo); err != nil {
		return err
	}
	kept := jobs.Items[:0]
	for i := range jobs.Items {
		if jobs.Items[i].Spec.MediaFileRef == ref {
			kept = append(kept, jobs.Items[i])
		}
	}
	jobs.Items = kept
	return nil
}
