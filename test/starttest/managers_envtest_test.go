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

package starttest_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/test/starttest"
)

// errRecorder captures Errorf instead of failing the test, so the helper's
// own failure can be asserted.
type errRecorder struct {
	testing.TB
	errs []string
}

func (r *errRecorder) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}
func (r *errRecorder) Helper() {}

// AssertWroteOnlyAs is what every RBAC-enforced start leans on to see an
// over-claim, so it is held to catching one: a write under a manager the
// identity may not use fails it by name, the same write under an identity
// that owns the manager does not, and the test's own unowned writes are
// ignored.
func TestAssertWroteOnlyAsCatchesAnUnlistedManager(t *testing.T) {
	env := starttest.StartEnv(t)
	admin := starttest.Client(t, env.Config)
	ctx := context.Background()

	movie := func(name string) *catalogv1alpha1.Movie {
		return &catalogv1alpha1.Movie{
			TypeMeta:   metav1.TypeMeta{APIVersion: catalogv1alpha1.GroupVersion.String(), Kind: "Movie"},
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       catalogv1alpha1.MovieSpec{TmdbID: 603, QualityProfileRef: "q", RootFolderRef: "r"},
		}
	}
	require.NoError(t, admin.Create(ctx, movie("existing")))

	rec := &errRecorder{TB: t}
	starttest.AssertWroteOnlyAs(rec, admin, "ui", func() {
		require.NoError(t, admin.Patch(ctx, movie("existing"), client.Apply, client.FieldOwner(string(k8s.ManagerCatalog)), client.ForceOwnership))
	})
	require.Len(t, rec.errs, 1, "a catalog write under identity ui")
	assert.Contains(t, rec.errs[0], `"catalog"`)
	assert.Contains(t, rec.errs[0], "Movie default/existing")

	rec = &errRecorder{TB: t}
	starttest.AssertWroteOnlyAs(rec, admin, "manager", func() {
		require.NoError(t, admin.Patch(ctx, movie("applied"), client.Apply, client.FieldOwner(string(k8s.ManagerImportWorker)), client.ForceOwnership))
		require.NoError(t, admin.Create(ctx, movie("unowned")))
	})
	assert.Empty(t, rec.errs, "the manager owns import-worker; the test's own create is ignored")

	rec = &errRecorder{TB: t}
	starttest.AssertWroteOnlyAs(rec, admin, "agent-import", func() {
		require.NoError(t, admin.Patch(ctx, movie("new"), client.Apply, client.FieldOwner(string(k8s.ManagerImportWorker)), client.ForceOwnership))
	})
	require.Len(t, rec.errs, 1, "an agent writes nothing, a created object included")
	assert.Contains(t, rec.errs[0], "Movie default/new")
}
