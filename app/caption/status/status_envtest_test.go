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

package status_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	subtitleac "github.com/mediactl/clustarr/api/applyconfiguration/subtitle/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/status"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })

	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

func newNamespace(t *testing.T, ctx context.Context, c client.Client, ns string) {
	t.Helper()
	require.NoError(t, client.IgnoreAlreadyExists(
		c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))
}

// SubtitleProfile and SubtitleProvider each have exactly one legitimate
// writer, but the same complete-declaration and managedFields-visibility
// rules apply: a second call site that forgot a field would silently narrow
// what is on the object, and only managedFields would show ManagerCaption
// owning less than it should.
func TestSubtitleProfileAndProviderRoundTripThroughManagerCaption(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	const ns = "caption-status-profile-provider"
	newNamespace(t, ctx, c, ns)

	profile := &subtitlev1alpha1.SubtitleProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: subtitlev1alpha1.SubtitleProfileSpec{
			Languages: []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en"}},
		},
	}
	require.NoError(t, c.Create(ctx, profile))
	require.NoError(t, status.PatchProfile(ctx, c, k8s.ManagerCaption, profile,
		func(ac *subtitleac.SubtitleProfileStatusApplyConfiguration) {
			ac.WithObservedGeneration(1).WithWantedKeys("en").WithConditions(k8s.ConditionAC(metav1.Condition{
				Type: subtitlev1alpha1.SubtitleProfileConditionReady, Status: metav1.ConditionTrue,
				Reason: "Ready", LastTransitionTime: metav1.Now(), ObservedGeneration: 1,
			}))
		}))

	var gotProfile subtitlev1alpha1.SubtitleProfile
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(profile), &gotProfile))
	assert.EqualValues(t, 1, gotProfile.Status.ObservedGeneration)
	assert.Equal(t, []string{"en"}, gotProfile.Status.WantedKeys)
	assert.NotEmpty(t, gotProfile.Status.Conditions)

	provider := &subtitlev1alpha1.SubtitleProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "opensubtitles", Namespace: ns},
		Spec:       subtitlev1alpha1.SubtitleProviderSpec{Type: subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom},
	}
	require.NoError(t, c.Create(ctx, provider))
	require.NoError(t, status.PatchProvider(ctx, c, k8s.ManagerCaption, provider,
		func(ac *subtitleac.SubtitleProviderStatusApplyConfiguration) {
			ac.WithObservedGeneration(1).WithErrorsLast120s(0).WithHIVerifiable(true)
		}))

	var gotProvider subtitlev1alpha1.SubtitleProvider
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(provider), &gotProvider))
	assert.EqualValues(t, 1, gotProvider.Status.ObservedGeneration)
	assert.True(t, gotProvider.Status.HIVerifiable)

	// A re-apply that only touches ThrottleReason must not release
	// HIVerifiable or ErrorsLast120s -- the same "survive its own re-apply"
	// property app/grab/status.status_envtest_test proves for the controller
	// and engine halves of Download.status.
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(provider), provider))
	require.NoError(t, status.PatchProvider(ctx, c, k8s.ManagerCaption, provider,
		func(ac *subtitleac.SubtitleProviderStatusApplyConfiguration) {
			ac.WithThrottleReason("5 errors in 120s")
		}))

	var afterReapply subtitlev1alpha1.SubtitleProvider
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(provider), &afterReapply))
	assert.True(t, afterReapply.Status.HIVerifiable, "the re-apply released hiVerifiable")
	assert.Equal(t, "5 errors in 120s", afterReapply.Status.ThrottleReason)

	err := status.PatchProvider(ctx, c, k8s.ManagerCaptionWorker, provider, nil)
	require.ErrorContains(t, err, "owns no part of SubtitleProvider.status")
}
