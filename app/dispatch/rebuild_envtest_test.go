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

package dispatch_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/dispatch"
	"github.com/mediactl/clustarr/app/remediation/search"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestTheLedgerIsRebuiltFromStatusNeverTrusted (§5.4, §8.4): a leader that
// starts with stale in-memory state -- a dispatch status says was answered, one
// for a CR that does not exist, one at an older seq than status's -- replaces
// it with what the items' searchDispatch blocks say is in flight, read through
// the search stage's own Source.
func TestTheLedgerIsRebuiltFromStatusNeverTrusted(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)

	ctx := t.Context()
	const ns = "dispatch-rebuild"
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	movie := func(name string, tmdbID int64, d *catalogac.DispatchApplyConfiguration) {
		t.Helper()
		require.NoError(t, c.Create(ctx, &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: catalogv1alpha1.MovieSpec{
				TmdbID: tmdbID, QualityProfileRef: "hd", RootFolderRef: "movies",
				MinimumAvailability: catalogv1alpha1.MinimumAvailabilityTBA,
			},
		}))
		if d == nil {
			return
		}
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalog,
			catalogac.Movie(name, ns).WithStatus(catalogac.MovieStatus().WithSearchDispatch(d)))
		require.NoError(t, err)
	}
	at := metav1.NewTime(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	movie("heat", 949, catalogac.Dispatch().WithSeq(3).WithAnsweredSeq(2).WithDispatchedAt(at))
	movie("ronin", 8195, catalogac.Dispatch().WithSeq(7).WithAnsweredSeq(5).WithDispatchedAt(at))
	movie("collateral", 1538, catalogac.Dispatch().WithSeq(2).WithAnsweredSeq(2).WithDispatchedAt(at))
	movie("thief", 11524, nil)

	key := func(name string) dispatch.Key {
		return dispatch.Key{Kind: "Movie", Namespace: ns, Name: name, Sub: "search"}
	}
	durable := search.LedgerSource{}.Durable()
	l := newLedger(clockwork.NewFakeClockAt(t0), dispatch.Options{Reader: c})
	l.Register(search.LedgerSource{})
	// What a previous term of this leader believed: none of it is status.
	l.Published(durable, key("collateral"), 2)
	l.Published(durable, key("ghost"), 9)
	l.Published(durable, key("heat"), 1)
	require.Equal(t, 3, l.Outstanding(durable))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- l.Start(runCtx) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})
	require.Eventually(t, l.Rebuilt, 10*time.Second, 10*time.Millisecond)

	assert.Equal(t, 2, l.Outstanding(durable), "heat at 3 and ronin at 7: what status says is in flight")
	l.Answered(durable, key("heat"), 2)
	assert.Equal(t, 2, l.Outstanding(durable), "heat's slot is status's seq 3, not the stale 1")
	l.Answered(durable, key("heat"), 3)
	l.Answered(durable, key("ronin"), 7)
	assert.Zero(t, l.Outstanding(durable), "the stale collateral and ghost dispatches are gone")
	for i := range l.Budget(durable) {
		require.True(t, l.Admit(durable, key("m"+string(rune('a'+i))), 1).Admitted,
			"the whole budget is free once status's dispatches are answered")
	}
}
