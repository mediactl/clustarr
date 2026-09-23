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

package subtitleprofile_test

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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/captionarr/controller/subtitleprofile"
	"github.com/mediactl/clustarr/pkg/k8s"
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

// movieFile creates a video-kind MediaFile. Unlike squasharr's
// TranscodeProfile (which only creates a TranscodeJob for a PROBED file,
// since pkg/transcode.Plan needs real MediaInfo), SubtitleProfile ensures a
// SubtitleRequest for every eligible file regardless of probe state -- the
// request's own planning is task F-4's job, not this controller's -- so
// these fixtures deliberately never touch status.probeHash unless a test is
// specifically about the probeHash watch.
//
// It creates the file's Movie too: a MediaFile whose item is gone is one an
// import list's removeAndKeep left behind, and no profile selects it.
func movieFile(t *testing.T, ctx context.Context, c client.Client, ns, name string, labels map[string]string) *catalogv1alpha1.MediaFile {
	t.Helper()
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 329865, QualityProfileRef: "hd", RootFolderRef: "movies"},
	}))
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name},
			Path:     "/data/movies/" + name + "/" + name + ".mkv",
		},
	}
	require.NoError(t, c.Create(ctx, mf))
	return mf
}

func defaultProfile(t *testing.T, ctx context.Context, c client.Client, name string) *subtitlev1alpha1.SubtitleProfile {
	t.Helper()
	sp := &subtitlev1alpha1.SubtitleProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: subtitlev1alpha1.SubtitleProfileSpec{
			Default:   true,
			Languages: []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en"}},
		},
	}
	require.NoError(t, c.Create(ctx, sp))
	return sp
}

func listRequests(t *testing.T, ctx context.Context, c client.Client) []subtitlev1alpha1.SubtitleRequest {
	t.Helper()
	var list subtitlev1alpha1.SubtitleRequestList
	require.NoError(t, c.List(ctx, &list))
	return list.Items
}

// TestReconcileEnsuresSubtitleRequestsIdempotently is the test the F-3 brief
// asks for by name: reconciling twice must not create a second
// SubtitleRequest for the same MediaFile, and every field this controller
// sets must be correct.
func TestReconcileEnsuresSubtitleRequestsIdempotently(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	const ns = "subtitleprofile-idempotent"
	require.NoError(t, client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))

	mf := movieFile(t, ctx, c, ns, "arrival-2016", nil)
	sp := defaultProfile(t, ctx, c, "default")

	r := subtitleprofile.NewReconciler(c, k8s.MustNewScheme(), events.NewFakeRecorder(10))
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: sp.Name}}

	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	first := listRequests(t, ctx, c)
	require.Len(t, first, 1, "one matching video file must ensure exactly one SubtitleRequest")

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	second := listRequests(t, ctx, c)
	require.Len(t, second, 1, "a second reconcile with nothing changed must create no new request")
	assert.Equal(t, first[0].Name, second[0].Name)

	sr := second[0]
	assert.Equal(t, mf.Name, sr.Name, "SubtitleRequest must share the MediaFile's name")
	assert.Equal(t, mf.Namespace, sr.Namespace)
	assert.Equal(t, mf.Name, sr.Spec.MediaFileRef)
	assert.Equal(t, sp.Name, sr.Spec.ProfileRef)
	require.Len(t, sr.OwnerReferences, 1, "the SubtitleRequest must be owned by the MediaFile")
	assert.Equal(t, mf.Name, sr.OwnerReferences[0].Name)
	assert.Equal(t, mf.UID, sr.OwnerReferences[0].UID)
	require.NotNil(t, sr.OwnerReferences[0].Controller)
	assert.True(t, *sr.OwnerReferences[0].Controller)

	// Ownership: this controller creates SubtitleRequest's MAIN resource
	// under k8s.ManagerCaptionarr, not its status -- assert the managedFields
	// entry directly rather than trusting only the values, per CLAUDE.md's
	// "an over-claim is silent" rule.
	var sawSpecOwner bool
	for _, mfEntry := range sr.ManagedFields {
		if mfEntry.Manager == string(k8s.ManagerCaptionarr) && mfEntry.Subresource == "" {
			sawSpecOwner = true
		}
	}
	assert.True(t, sawSpecOwner, "k8s.ManagerCaptionarr must own the SubtitleRequest main resource")

	var gotProfile subtitlev1alpha1.SubtitleProfile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: sp.Name}, &gotProfile))
	assert.EqualValues(t, 1, gotProfile.Status.MatchingFiles)
	assert.Equal(t, []string{"en"}, gotProfile.Status.WantedKeys)
	assert.True(t, k8s.IsConditionTrue(gotProfile.Status.Conditions, k8s.ConditionReady))
	assert.False(t, k8s.IsConditionTrue(gotProfile.Status.Conditions, subtitlev1alpha1.SubtitleProfileConditionInvalid))
}

// TestReconcileUpdatesProfileRefWhenTheWinningProfileChanges proves
// SubtitleRequestSpec.ProfileRef is NOT treated as create-once the way
// TranscodeJob's spec is: a later profile with a matching selector outranks
// the default fallback, and its reconcile must update the EXISTING
// SubtitleRequest's profileRef in place rather than creating a second
// request (SubtitleRequest is one-per-file by name, unlike TranscodeJob's
// per-profile-hash naming).
func TestReconcileUpdatesProfileRefWhenTheWinningProfileChanges(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	const ns = "subtitleprofile-reselect"
	require.NoError(t, client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))

	mf := movieFile(t, ctx, c, ns, "arrival-2016", map[string]string{"tier": "hd"})
	def := defaultProfile(t, ctx, c, "default")

	r := subtitleprofile.NewReconciler(c, k8s.MustNewScheme(), events.NewFakeRecorder(10))
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: def.Name}})
	require.NoError(t, err)

	before := listRequests(t, ctx, c)
	require.Len(t, before, 1)
	assert.Equal(t, def.Name, before[0].Spec.ProfileRef)

	selective := &subtitlev1alpha1.SubtitleProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "hd-selective"},
		Spec: subtitlev1alpha1.SubtitleProfileSpec{
			Selector:  &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "hd"}},
			Languages: []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en"}},
		},
	}
	require.NoError(t, c.Create(ctx, selective))
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: selective.Name}})
	require.NoError(t, err)

	after := listRequests(t, ctx, c)
	require.Len(t, after, 1, "the winning profile changing must update the existing request, not create a second one")
	assert.Equal(t, mf.Name, after[0].Name)
	assert.Equal(t, selective.Name, after[0].Spec.ProfileRef, "profileRef must follow the new winner")
}

// TestReconcileMarksTheNewerOfTwoDefaultsInvalid mirrors
// squasharr/controller/transcodeprofile's identical ruling for the identical
// shape shared between the two CRDs.
func TestReconcileMarksTheNewerOfTwoDefaultsInvalid(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	const ns = "subtitleprofile-dup-default"
	require.NoError(t, client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))

	movieFile(t, ctx, c, ns, "arrival-2016", nil)

	first := defaultProfile(t, ctx, c, "first")
	time.Sleep(1100 * time.Millisecond) // CreationTimestamp has 1s resolution
	second := defaultProfile(t, ctx, c, "second")

	r := subtitleprofile.NewReconciler(c, k8s.MustNewScheme(), events.NewFakeRecorder(10))
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: first.Name}})
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: second.Name}})
	require.NoError(t, err)

	var gotFirst, gotSecond subtitlev1alpha1.SubtitleProfile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: first.Name}, &gotFirst))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: second.Name}, &gotSecond))

	assert.False(t, k8s.IsConditionTrue(gotFirst.Status.Conditions, subtitlev1alpha1.SubtitleProfileConditionInvalid),
		"the earlier-created default must remain valid")
	assert.True(t, k8s.IsConditionTrue(gotSecond.Status.Conditions, subtitlev1alpha1.SubtitleProfileConditionInvalid),
		"the later-created default must be marked Invalid")

	requests := listRequests(t, ctx, c)
	if assert.Len(t, requests, 1, "the valid default profile must still ensure its one request") {
		assert.Equal(t, first.Name, requests[0].Spec.ProfileRef, "the Invalid profile must not have claimed the request")
	}
}

// TestReconcileSurfacesSelectorOverlapWithoutDoubleEnsuring is the plan's
// explicit ruling: when two profiles' selectors match the same file, the
// deterministic winner ensures the one request and the loser ensures none
// but reports Overlap=True on itself.
func TestReconcileSurfacesSelectorOverlapWithoutDoubleEnsuring(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	const ns = "subtitleprofile-overlap"
	require.NoError(t, client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))

	movieFile(t, ctx, c, ns, "arrival-2016", map[string]string{"tier": "hd"})

	sel := &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "hd"}}
	winner := &subtitlev1alpha1.SubtitleProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "hd-a"},
		Spec:       subtitlev1alpha1.SubtitleProfileSpec{Selector: sel, Languages: []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en"}}},
	}
	require.NoError(t, c.Create(ctx, winner))
	time.Sleep(1100 * time.Millisecond)
	loser := &subtitlev1alpha1.SubtitleProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "hd-b"},
		Spec:       subtitlev1alpha1.SubtitleProfileSpec{Selector: sel, Languages: []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en"}}},
	}
	require.NoError(t, c.Create(ctx, loser))

	r := subtitleprofile.NewReconciler(c, k8s.MustNewScheme(), events.NewFakeRecorder(10))
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: winner.Name}})
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: loser.Name}})
	require.NoError(t, err)

	requests := listRequests(t, ctx, c)
	require.Len(t, requests, 1, "an overlapping selector must never create two requests for one file")
	assert.Equal(t, winner.Name, requests[0].Spec.ProfileRef)

	var gotWinner, gotLoser subtitlev1alpha1.SubtitleProfile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: winner.Name}, &gotWinner))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: loser.Name}, &gotLoser))
	assert.False(t, k8s.IsConditionTrue(gotWinner.Status.Conditions, subtitleprofile.ConditionOverlap))
	assert.True(t, k8s.IsConditionTrue(gotLoser.Status.Conditions, subtitleprofile.ConditionOverlap),
		"the losing profile must surface the overlap on its own status")
	assert.EqualValues(t, 0, gotLoser.Status.MatchingFiles)
}

// TestReconcileRejectsLanguageKeyMismatch proves a profile whose declared
// key does not match its canonical derivation is Invalid and ensures no
// SubtitleRequest, even though it otherwise matches a file (it is also the
// cluster default here) -- pkg/subtitles/planner.go's Plan trusts every
// LanguageItem.Key it is handed rather than re-deriving it, specifically
// because this controller is documented as the enforcement point.
func TestReconcileRejectsLanguageKeyMismatch(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	const ns = "subtitleprofile-key-mismatch"
	require.NoError(t, client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))

	movieFile(t, ctx, c, ns, "arrival-2016", nil)

	sp := &subtitlev1alpha1.SubtitleProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "mismatched"},
		Spec: subtitlev1alpha1.SubtitleProfileSpec{
			Default: true,
			// "pt-BR" combined with hi=required must canonicalize to
			// "pt-BR:hi", not "pt-BR" -- see LanguageItem.Key's own doc
			// comment.
			Languages: []subtitlev1alpha1.LanguageItem{{Key: "pt-BR", Language: "pt-BR", HI: subtitlev1alpha1.HIPolicyRequired}},
		},
	}
	require.NoError(t, c.Create(ctx, sp))

	r := subtitleprofile.NewReconciler(c, k8s.MustNewScheme(), events.NewFakeRecorder(10))
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: sp.Name}})
	require.NoError(t, err)

	var got subtitlev1alpha1.SubtitleProfile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: sp.Name}, &got))
	require.True(t, k8s.IsConditionTrue(got.Status.Conditions, subtitlev1alpha1.SubtitleProfileConditionInvalid))
	cond := k8s.FindCondition(got.Status.Conditions, subtitlev1alpha1.SubtitleProfileConditionInvalid)
	require.NotNil(t, cond)
	assert.Equal(t, subtitleprofile.ReasonKeyMismatch, cond.Reason)

	assert.Empty(t, listRequests(t, ctx, c), "an Invalid profile must never ensure a SubtitleRequest")
}
