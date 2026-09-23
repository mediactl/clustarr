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

package actions_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/ui/actions"
)

// recordingPatcher wraps a real client.Client and records the raw bytes of
// every merge patch it sends, while still forwarding the call to the real
// apiserver so this file's apiserver-side assertions stay meaningful.
type recordingPatcher struct {
	c      client.Client
	bodies [][]byte
}

func (r *recordingPatcher) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	data, err := patch.Data(obj)
	if err != nil {
		return err
	}
	r.bodies = append(r.bodies, data)
	return r.c.Patch(ctx, obj, patch, opts...)
}

// TestSetSubtitleProviderSettingsReallyDisablesAgainstARealAPIServer is the
// coordinator's own falsification requirement for this task: settings.go's
// patch bodies are hand-written structs with no `omitempty` tag (unlike the
// generated subtitlev1alpha1.SubtitleProviderSpec as it was then, whose
// Enabled field was `bool json:"enabled,omitempty"` until G4-0 made it a
// *bool), precisely so that setting enabled=false is not silently dropped
// from the merge patch's JSON the way it would be if this action marshalled
// the generated spec type directly. This test proves
// two things together: the bytes actually sent to Patch carry
// "enabled":false, and a real apiserver applies that merge patch and leaves
// the field false -- not re-defaulted back to true by CRD defaulting, and
// not dropped by any strategic-merge behaviour SubtitleProvider does not
// even opt into (it carries no patchStrategy on spec.enabled).
//
// Falsified directly (while Enabled was still a plain bool): temporarily
// marshalling the generated subtitlev1alpha1.SubtitleProviderSpec as the
// patch body instead of the hand-written enabledPriorityPatch reproduced
// exactly the bug this test exists to catch -- the merge patch's JSON
// silently loses "enabled":false, the object re-reads as enabled=true, and
// both assertions below fail. Confirmed by editing settings.go's
// SetSubtitleProviderSettings to build its body from a
// subtitlev1alpha1.SubtitleProviderSpec{Enabled: enabled, Priority:
// priority} value instead of enabledPriorityPatch, then reverting once this
// test failed as expected.
func TestSetSubtitleProviderSettingsReallyDisablesAgainstARealAPIServer(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}

	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })

	scheme := runtime.NewScheme()
	require.NoError(t, subtitlev1alpha1.AddToScheme(scheme))
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)

	ctx := t.Context()
	const ns = "default"

	provider := &subtitlev1alpha1.SubtitleProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "opensubtitlescom", Namespace: ns},
		Spec: subtitlev1alpha1.SubtitleProviderSpec{
			Type:     subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom,
			Enabled:  ptr.To(true),
			Priority: 50,
		},
	}
	require.NoError(t, c.Create(ctx, provider, client.FieldOwner("test-creator")))

	rec := &recordingPatcher{c: c}
	_, err = actions.SetSubtitleProviderSettings(ctx, rec, ns, provider.Name, false, 90)
	require.NoError(t, err)

	// The bytes Patch actually received must carry "enabled":false
	// literally -- the exact thing an omitempty-tagged typed patch body
	// would drop.
	require.Len(t, rec.bodies, 1)
	require.JSONEq(t, `{"spec":{"enabled":false,"priority":90}}`, string(rec.bodies[0]))

	// And a real apiserver must have applied it: re-Get the object and
	// check spec.enabled is really false, not left at (or re-defaulted to)
	// true.
	var got subtitlev1alpha1.SubtitleProvider
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: provider.Name}, &got))
	require.NotNil(t, got.Spec.Enabled)
	require.False(t, *got.Spec.Enabled,
		"spec.enabled must really be false after the merge patch, not silently re-defaulted or dropped "+
			"back to the CRD's enabled=true default -- see this test's own doc comment for how a "+
			"typed-struct-with-omitempty patch body would fail it")
	require.Equal(t, int32(90), got.Spec.Priority)
}
