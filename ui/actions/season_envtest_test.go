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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/ui/actions"
)

// interposedGetter runs after between a Get returning and the caller's next
// step -- the window a read-then-patch action has to lose an update in.
type interposedGetter struct {
	client.Reader
	after func()
}

func (g *interposedGetter) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := g.Reader.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	if g.after != nil {
		g.after()
	}
	return nil
}

func seasonFlags(s *catalogv1alpha1.Series) map[int32]bool {
	out := map[int32]bool{}
	for _, sp := range s.Spec.Seasons {
		out[sp.Number] = ptr.Deref(sp.Monitored, true)
	}
	return out
}

// SetSeasonMonitored (spec 2026-09-23-library-page-design, actions) sets
// one season's override and leaves every other entry as it was, adds an
// entry for a season with none, writes under the UI's manager alone, and --
// because a merge patch replaces the whole list -- carries the
// resourceVersion it read, so a real second writer landing between the read
// and the patch is retried onto rather than overwritten.
func TestSetSeasonMonitoredKeepsTheOtherSeasonsAndSurvivesARacingWriter(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1alpha1.AddToScheme(scheme))
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	ctx := t.Context()
	const ns = "default"

	series := &catalogv1alpha1.Series{ObjectMeta: meta("simpsons", ns), Spec: catalogv1alpha1.SeriesSpec{
		TvdbID: 71663, QualityProfileRef: "web-1080p", RootFolderRef: "tv",
		Seasons: []catalogv1alpha1.SeasonSpec{{Number: 1, Monitored: ptr.To(true)}, {Number: 3, Monitored: ptr.To(false)}},
	}}
	require.NoError(t, c.Create(ctx, series, client.FieldOwner(creatorManager)))
	gvk := mustGVK(t, series, scheme)
	seedStatus(ctx, t, c, k8s.ManagerCatalogarr, gvk, series.Name, ns)
	rec := &recordingWriter{c: c}

	got, err := actions.SetSeasonMonitored(ctx, c, rec, ns, "simpsons", 2, false)
	require.NoError(t, err)
	require.Equal(t, map[int32]bool{1: true, 2: false, 3: false}, seasonFlags(got), "season 2 added, the rest untouched")

	got, err = actions.SetSeasonMonitored(ctx, c, rec, ns, "simpsons", 3, true)
	require.NoError(t, err)
	require.Equal(t, map[int32]bool{1: true, 2: false, 3: true}, seasonFlags(got), "season 3 flipped, the rest untouched")

	after := getUnstructured(ctx, t, c, gvk, "simpsons", ns)
	requireNeverOnStatus(t, after)
	entry := requireOneUIEntry(t, after)
	requireFieldsContain(t, entry, "f:spec", "f:seasons")
	requireStatusStillOwnedBy(t, after, k8s.ManagerCatalogarr.String())

	// A real second writer unmonitors season 1 after the action has read
	// the Series and before it patches: the first patch must be refused
	// (its resourceVersion is stale), and the retry must land both changes.
	racing := 0
	getter := &interposedGetter{Reader: c, after: func() {
		if racing > 0 {
			return
		}
		racing++
		var cur catalogv1alpha1.Series
		require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "simpsons"}, &cur))
		cur.Spec.Seasons[0].Monitored = ptr.To(false)
		require.NoError(t, c.Update(ctx, &cur, client.FieldOwner(creatorManager)))
	}}
	got, err = actions.SetSeasonMonitored(ctx, getter, rec, ns, "simpsons", 2, true)
	require.NoError(t, err)
	require.Equal(t, map[int32]bool{1: false, 2: true, 3: true}, seasonFlags(got),
		"the racing writer's season 1 and this action's season 2 both land")
	require.Equal(t, 1, racing, "the retry re-read once")

	// A writer that races every attempt exhausts the one retry: a conflict,
	// reported, never a silent overwrite.
	always := &interposedGetter{Reader: c, after: func() {
		var cur catalogv1alpha1.Series
		require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "simpsons"}, &cur))
		cur.Spec.Tags = append(cur.Spec.Tags, "bump")
		require.NoError(t, c.Update(ctx, &cur, client.FieldOwner(creatorManager)))
	}}
	_, err = actions.SetSeasonMonitored(ctx, always, rec, ns, "simpsons", 2, false)
	require.True(t, apierrors.IsConflict(err), "want a conflict, got %v", err)

	_, err = actions.SetSeasonMonitored(ctx, c, rec, ns, "never-existed", 1, false)
	require.True(t, apierrors.IsNotFound(err), "want NotFound, got %v", err)
}
