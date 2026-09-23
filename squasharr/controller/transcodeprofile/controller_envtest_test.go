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

package transcodeprofile_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/squasharr/controller/transcodeprofile"
)

func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })

	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

// probedMovie creates a MediaFile that already looks probed -- ProbeHash and
// MediaInfo set -- which is what makes it eligible for a TranscodeJob at all
// (see profile.go's probed helper). Every test in this file uses the same
// namespace/labels shape so TranscodeJob's owner reference and the
// mediaFileRef/sourcePath/sourceProbeHash fields can be asserted precisely.
//
// The status write goes through k8s.PatchStatus under k8s.ManagerCatalogarr
// (a hand-built fixture standing in for catalogarr's own mediafile
// controller) rather than a direct client.Status().Update -- forbidigo has
// no test-file exemption in .golangci.yml, and every other envtest suite in
// this tree that needs to seed a status uses the real apply path instead of
// suppressing the lint.
func probedMovie(t *testing.T, ctx context.Context, c client.Client, ns, name string, labels map[string]string) *catalogv1alpha1.MediaFile {
	t.Helper()
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name},
			Path:     "/data/movies/" + name + "/" + name + ".mkv",
		},
	}
	require.NoError(t, c.Create(ctx, mf))

	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr,
		catalogac.MediaFile(mf.Name, mf.Namespace).WithStatus(
			catalogac.MediaFileStatus().
				WithProbeHash("probe-"+name).
				WithMediaInfo(commonv1.MediaInfo{})))
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, mf))
	return mf
}

// tagMovieAsTranscoded patches mf.status.transcode.profileTag, standing in
// for catalogarr's mediafile controller incorporating a successful swap
// (mediafile_controller.go:275-287).
func tagMovieAsTranscoded(t *testing.T, ctx context.Context, c client.Client, mf *catalogv1alpha1.MediaFile, tag string) {
	t.Helper()
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr,
		catalogac.MediaFile(mf.Name, mf.Namespace).WithStatus(
			catalogac.MediaFileStatus().
				WithProbeHash(mf.Status.ProbeHash).
				WithMediaInfo(commonv1.MediaInfo{}).
				WithTranscode(catalogac.TranscodeState().WithCompliant(true).WithProfileTag(tag))))
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: mf.Name, Namespace: mf.Namespace}, mf))
}

func defaultProfile(t *testing.T, ctx context.Context, c client.Client, name string) *transcodev1alpha1.TranscodeProfile {
	t.Helper()
	tp := &transcodev1alpha1.TranscodeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       transcodev1alpha1.TranscodeProfileSpec{Default: true},
	}
	require.NoError(t, c.Create(ctx, tp))
	return tp
}

func listJobs(t *testing.T, ctx context.Context, c client.Client) []transcodev1alpha1.TranscodeJob {
	t.Helper()
	var list transcodev1alpha1.TranscodeJobList
	require.NoError(t, c.List(ctx, &list))
	return list.Items
}

// TestReconcileCreatesJobsIdempotently is the test the Phase E plan asks for
// by name: reconciling twice must not create a second TranscodeJob for the
// same (MediaFile, profile hash) pair, because transcodeJobName is
// deterministic on exactly that pair and k8s.Apply is a no-op re-apply
// against an object that already exists.
func TestReconcileCreatesJobsIdempotently(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	const ns = "transcodeprofile-idempotent"
	require.NoError(t, client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))

	mf := probedMovie(t, ctx, c, ns, "arrival-2016", nil)
	tp := defaultProfile(t, ctx, c, "default")

	r := transcodeprofile.NewReconciler(c, k8s.MustNewScheme(), events.NewFakeRecorder(10))
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: tp.Name}}

	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	first := listJobs(t, ctx, c)
	require.Len(t, first, 1, "one matching, probed file must create exactly one TranscodeJob")

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	second := listJobs(t, ctx, c)
	require.Len(t, second, 1, "a second reconcile with nothing changed must create no new job")
	assert.Equal(t, first[0].Name, second[0].Name, "re-reconciling must not rename or duplicate the job")

	job := second[0]
	assert.Equal(t, mf.Name, job.Spec.MediaFileRef)
	assert.Equal(t, tp.Name, job.Spec.ProfileRef)
	assert.Equal(t, mf.Spec.Path, job.Spec.SourcePath)
	assert.Equal(t, mf.Status.ProbeHash, job.Spec.SourceProbeHash)
	require.Len(t, job.OwnerReferences, 1, "the TranscodeJob must be owned by the MediaFile")
	assert.Equal(t, mf.Name, job.OwnerReferences[0].Name)
	assert.Equal(t, mf.UID, job.OwnerReferences[0].UID)
	assert.True(t, *job.OwnerReferences[0].Controller)

	// Ownership: this controller creates TranscodeJob's MAIN resource under
	// k8s.ManagerSquasharr, not its status -- assert the managedFields entry
	// directly rather than trusting only the values, per CLAUDE.md's "an
	// over-claim is silent" rule.
	var sawSpecOwner bool
	for _, mfEntry := range job.ManagedFields {
		if mfEntry.Manager == string(k8s.ManagerSquasharr) && mfEntry.Subresource == "" {
			sawSpecOwner = true
		}
	}
	assert.True(t, sawSpecOwner, "k8s.ManagerSquasharr must own the TranscodeJob main resource")

	var gotProfile transcodev1alpha1.TranscodeProfile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: tp.Name}, &gotProfile))
	assert.EqualValues(t, 1, gotProfile.Status.MatchingFiles)
	assert.NotEmpty(t, gotProfile.Status.Hash)
	assert.True(t, k8s.IsConditionTrue(gotProfile.Status.Conditions, k8s.ConditionReady))
	assert.False(t, k8s.IsConditionTrue(gotProfile.Status.Conditions, transcodev1alpha1.TranscodeProfileConditionInvalid))
}

// TestReconcileProfileEditCreatesExactlyOneNewJob is the plan's second named
// case: editing the profile changes status.hash, which changes
// transcodeJobName's suffix, which must create exactly one additional
// TranscodeJob -- the old one is left alone (it describes the OLD hash's
// plan and its spec is CEL-immutable) rather than mutated or duplicated.
func TestReconcileProfileEditCreatesExactlyOneNewJob(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	const ns = "transcodeprofile-edit"
	require.NoError(t, client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))

	probedMovie(t, ctx, c, ns, "arrival-2016", nil)
	tp := defaultProfile(t, ctx, c, "default")

	r := transcodeprofile.NewReconciler(c, k8s.MustNewScheme(), events.NewFakeRecorder(10))
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: tp.Name}}

	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	before := listJobs(t, ctx, c)
	require.Len(t, before, 1)

	var fresh transcodev1alpha1.TranscodeProfile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: tp.Name}, &fresh))
	fresh.Spec.Video.Preset = "veryslow" // any render-relevant field changes ProfileHash
	require.NoError(t, c.Update(ctx, &fresh))

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	after := listJobs(t, ctx, c)
	require.Len(t, after, 2, "a profile edit must create exactly one additional job, not replace or duplicate the first")

	names := map[string]bool{after[0].Name: true, after[1].Name: true}
	assert.True(t, names[before[0].Name], "the original job must still exist, unmutated")
	assert.Len(t, names, 2, "the edit must not have renamed or collapsed the two jobs onto one name")
}

// TestReconcileSkipsAFileAlreadyTaggedWithTheCurrentHash proves the tag
// check that keeps this controller from re-transcoding a file forever: once
// catalogarr's mediafile controller has incorporated a successful swap and
// written status.transcode.profileTag, a MediaFile carrying THIS profile's
// current tag creates no job at all.
func TestReconcileSkipsAFileAlreadyTaggedWithTheCurrentHash(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	const ns = "transcodeprofile-tagged"
	require.NoError(t, client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))

	tp := defaultProfile(t, ctx, c, "default")

	// First reconcile establishes status.hash.
	r := transcodeprofile.NewReconciler(c, k8s.MustNewScheme(), events.NewFakeRecorder(10))
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: tp.Name}}
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	var seededProfile transcodev1alpha1.TranscodeProfile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: tp.Name}, &seededProfile))
	require.NotEmpty(t, seededProfile.Status.Hash)

	mf := probedMovie(t, ctx, c, ns, "arrival-2016", nil)
	tagMovieAsTranscoded(t, ctx, c, mf, tp.Name+"@"+seededProfile.Status.Hash)

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Empty(t, listJobs(t, ctx, c), "a file already tagged with the current profile hash must get no job")
}

// TestReconcileMarksTheNewerOfTwoDefaultsInvalid exercises the CRD's own
// documented rule and profile.go's validateProfile: exactly one profile may
// be spec.default; the later-created one is Invalid and creates no jobs.
func TestReconcileMarksTheNewerOfTwoDefaultsInvalid(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	const ns = "transcodeprofile-dup-default"
	require.NoError(t, client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))

	probedMovie(t, ctx, c, ns, "arrival-2016", nil)

	first := defaultProfile(t, ctx, c, "first")
	time.Sleep(1100 * time.Millisecond) // CreationTimestamp has 1s resolution
	second := defaultProfile(t, ctx, c, "second")

	r := transcodeprofile.NewReconciler(c, k8s.MustNewScheme(), events.NewFakeRecorder(10))
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: first.Name}})
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: second.Name}})
	require.NoError(t, err)

	var gotFirst, gotSecond transcodev1alpha1.TranscodeProfile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: first.Name}, &gotFirst))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: second.Name}, &gotSecond))

	assert.False(t, k8s.IsConditionTrue(gotFirst.Status.Conditions, transcodev1alpha1.TranscodeProfileConditionInvalid),
		"the earlier-created default must remain valid")
	assert.True(t, k8s.IsConditionTrue(gotSecond.Status.Conditions, transcodev1alpha1.TranscodeProfileConditionInvalid),
		"the later-created default must be marked Invalid")

	// The valid profile ("first") still creates its one legitimate job; the
	// Invalid one ("second") must create none at all, even though it also
	// "matches" the file by falling back to spec.default -- so there must be
	// exactly one job in total, and it must belong to "first".
	jobs := listJobs(t, ctx, c)
	if assert.Len(t, jobs, 1, "the valid default profile must still create its one job") {
		assert.Equal(t, first.Name, jobs[0].Spec.ProfileRef, "the Invalid profile must not have created a job")
	}
}

// TestReconcileSurfacesSelectorOverlapWithoutDoubleCreating is the plan's
// explicit ruling: when two profiles' selectors match the same file, the
// deterministic winner creates the one job and the loser creates none but
// reports Overlap=True on itself.
func TestReconcileSurfacesSelectorOverlapWithoutDoubleCreating(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	const ns = "transcodeprofile-overlap"
	require.NoError(t, client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))

	probedMovie(t, ctx, c, ns, "arrival-2016", map[string]string{"tier": "hd"})

	sel := &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "hd"}}
	winner := &transcodev1alpha1.TranscodeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "hd-a"},
		Spec:       transcodev1alpha1.TranscodeProfileSpec{Selector: sel},
	}
	require.NoError(t, c.Create(ctx, winner))
	time.Sleep(1100 * time.Millisecond)
	loser := &transcodev1alpha1.TranscodeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "hd-b"},
		Spec:       transcodev1alpha1.TranscodeProfileSpec{Selector: sel},
	}
	require.NoError(t, c.Create(ctx, loser))

	r := transcodeprofile.NewReconciler(c, k8s.MustNewScheme(), events.NewFakeRecorder(10))
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: winner.Name}})
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: loser.Name}})
	require.NoError(t, err)

	jobs := listJobs(t, ctx, c)
	require.Len(t, jobs, 1, "an overlapping selector must never create two jobs for one file")
	assert.Equal(t, winner.Name, jobs[0].Spec.ProfileRef)

	var gotWinner, gotLoser transcodev1alpha1.TranscodeProfile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: winner.Name}, &gotWinner))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: loser.Name}, &gotLoser))
	assert.False(t, k8s.IsConditionTrue(gotWinner.Status.Conditions, transcodeprofile.ConditionOverlap))
	assert.True(t, k8s.IsConditionTrue(gotLoser.Status.Conditions, transcodeprofile.ConditionOverlap),
		"the losing profile must surface the overlap on its own status")
	assert.EqualValues(t, 0, gotLoser.Status.MatchingFiles)
}
