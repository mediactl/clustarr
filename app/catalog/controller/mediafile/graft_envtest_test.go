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
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/transcode/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/app/import/worker/rescan"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// succeededGraft records squasharr's report of a graft of mfName that
// finished after the steady state's probe.
func succeededGraft(t *testing.T, ctx context.Context, c client.Client, ns, mfName, tag string) {
	t.Helper()
	time.Sleep(1100 * time.Millisecond) // metav1.Time has whole seconds
	g := &transcodev1alpha1.AudioGraft{
		ObjectMeta: metav1.ObjectMeta{Name: k8s.AudioGraftName("inception"), Namespace: ns},
		Spec: transcodev1alpha1.AudioGraftSpec{
			ItemRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "inception"}, DonorPath: "/data/media/movies/.clustarr/d.mkv",
			Languages: []string{"en"}, Anchor: "ja", Release: "r",
		},
	}
	require.NoError(t, client.IgnoreAlreadyExists(c.Create(ctx, g)))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerSquasharr, transcodeac.AudioGraft(g.Name, ns).WithStatus(
		transcodeac.AudioGraftStatus().WithPhase(transcodev1alpha1.AudioGraftSucceeded).WithReason("Grafted").
			WithMediaFileRef(mfName).WithGraftTag(tag).WithCompletedAt(metav1.NewTime(time.Now()))))
	require.NoError(t, err)
}

// TestAGraftSwapIsIncorporatedWithoutMakingATranscode: squasharr's graft
// rewrote the file in place. catalogarr re-probes it, records the graft,
// and takes over the size, mtime and path as after a transcode swap -- but
// never spec.original, which importarr keeps: a graft is not a transcode
// (spec §7.2, §8), and the file stays upgradable.
func TestAGraftSwapIsIncorporatedWithoutMakingATranscode(t *testing.T) {
	ctx := context.Background()
	c, _ := startEnv(t)
	const ns = "graft-swap-ns"
	mustNamespace(t, ctx, c, ns)
	dir := t.TempDir()
	source := writeFile(t, dir, "Inception (2010).mkv", []byte("the imported file"))

	r := &mediafile.Reconciler{Client: c, Recorder: events.NewFakeRecorder(64), Probe: fakeProbe, Clock: time.Now}
	req := transcodeSteadyState(t, ctx, c, r, ns, "inception-graft", source)
	var before catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, req.NamespacedName, &before))

	// squasharr's graft Job: the same path, new bytes.
	grafted := []byte("the imported file, with an English dub muxed in")
	require.NoError(t, os.WriteFile(source, grafted, 0o644))
	succeededGraft(t, ctx, c, ns, "inception-graft", "61d29fba5193")

	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	var mf catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, req.NamespacedName, &mf))
	assert.Equal(t, "61d29fba5193", mf.Status.GraftTag)
	assert.NotNil(t, mf.Status.GraftedAt)
	assert.NotEqual(t, before.Status.ProbeHash, mf.Status.ProbeHash, "the new bytes are probed")
	assert.EqualValues(t, len(grafted), mf.Spec.SizeBytes, "catalogarr records the grafted size")
	require.NotNil(t, mf.Spec.Original)
	assert.True(t, *mf.Spec.Original, "a graft is not a transcode")
	assert.False(t, mf.Transcoded())
	assert.Nil(t, mf.Status.Transcode, "and is not judged as one")

	catalogarrSpec := specFieldNames(managedFieldPaths(mf.ManagedFields, "catalogarr", ""))
	assert.True(t, catalogarrSpec["sizeBytes"], "catalogarr owns spec.sizeBytes after a graft: %v", catalogarrSpec)
	assert.True(t, catalogarrSpec["modTime"], "and spec.modTime: %v", catalogarrSpec)
	assert.False(t, catalogarrSpec["original"], "never spec.original: %v", catalogarrSpec)
	importarrSpec := specFieldNames(managedFieldPaths(mf.ManagedFields, rescan.FieldManager.String(), ""))
	assert.True(t, importarrSpec["original"], "importarr keeps spec.original: %v", importarrSpec)

	// A second pass incorporates nothing again and keeps the takeover.
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	var again catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, req.NamespacedName, &again))
	assert.Equal(t, mf.Status.ProbeHash, again.Status.ProbeHash)
	assert.Equal(t, mf.Status.GraftedAt, again.Status.GraftedAt, "the graft is incorporated once")
	assert.True(t, specFieldNames(managedFieldPaths(again.ManagedFields, "catalogarr", ""))["sizeBytes"])
}
