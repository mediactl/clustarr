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

package k8s_test

import (
	"context"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// newTestClient starts an apiserver with the Clustarr CRDs installed and
// returns a client using the project scheme. Like pkg/crdcheck it needs the
// envtest control-plane binaries and skips without them, so `go test ./...` on
// a bare checkout still passes; `make test` sets KUBEBUILDER_ASSETS.
func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})

	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return c
}

// newMovie creates the object the status tests apply to. It was a
// Download until the ADR-0019 cold turkey deleted that kind (A9.1); a Movie
// carries status from two catalog managers -- ManagerCatalog's phase and
// path, ManagerCatalogGrab's search attempts -- which is what these tests
// need.
func newMovie(t *testing.T, ctx context.Context, c client.Client, ns, name string) *catalogv1alpha1.Movie {
	t.Helper()

	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil && !isAlreadyExists(err) {
		t.Fatalf("create namespace: %v", err)
	}

	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID:            27205,
			QualityProfileRef: "hd-bluray-web",
			RootFolderRef:     "movies",
		},
	}
	if err := c.Create(ctx, m); err != nil {
		t.Fatalf("create Movie: %v", err)
	}
	return m
}

func isAlreadyExists(err error) bool {
	return err != nil && client.IgnoreAlreadyExists(err) == nil
}

// TestPatchStatusAppliesToTheStatusSubresource is the basic contract: the
// status lands, the spec is untouched, and the write is attributed to the
// field manager the caller named.
func TestPatchStatusAppliesToTheStatusSubresource(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "patchstatus", "inception-abc1234567"

	newMovie(t, ctx, c, ns, name)

	ac := catalogac.Movie(name, ns).WithStatus(
		catalogac.MovieStatus().
			WithPhase(catalogv1alpha1.MoviePhaseWanted).
			WithPath("/data/media/movies/Inception (2010)").
			WithObservedGeneration(1),
	)
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalog, ac); err != nil {
		t.Fatalf("PatchStatus: %v", err)
	}

	var got catalogv1alpha1.Movie
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != catalogv1alpha1.MoviePhaseWanted {
		t.Errorf("phase = %q, want Wanted", got.Status.Phase)
	}
	if got.Status.Path != "/data/media/movies/Inception (2010)" {
		t.Errorf("path = %q", got.Status.Path)
	}
	if got.Spec.TmdbID != 27205 {
		t.Error("the spec was cleared by a status apply")
	}

	if !managesField(got.ManagedFields, "catalog", "status") {
		t.Errorf("no catalog-owned status entry in managedFields: %+v", fieldManagerNames(got.ManagedFields))
	}
}

// TestPatchStatusTwoManagersDoNotClobber is §3's single-writer rule as the
// apiserver enforces it: catalog owns phase and path, catalog-grab owns the
// search attempts, and each may re-apply its own fields with
// force-ownership without erasing the other's.
func TestPatchStatusTwoManagersDoNotClobber(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "noclobber", "inception-abc1234567"

	newMovie(t, ctx, c, ns, name)

	controller := catalogac.Movie(name, ns).WithStatus(
		catalogac.MovieStatus().WithPhase(catalogv1alpha1.MoviePhaseWanted).WithPath("/data/media/movies/Inception (2010)"),
	)
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalog, controller); err != nil {
		t.Fatalf("controller PatchStatus: %v", err)
	}

	searched := metav1.Now()
	grab := catalogac.Movie(name, ns).WithStatus(
		catalogac.MovieStatus().
			WithLastSearchedAt(searched).
			WithSearchAttempts(commonv1alpha1.Attempts{Count: 1}),
	)
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogGrab, grab); err != nil {
		t.Fatalf("grab PatchStatus: %v", err)
	}

	var got catalogv1alpha1.Movie
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != catalogv1alpha1.MoviePhaseWanted {
		t.Errorf("the grab stage's apply erased the controller's phase: %q", got.Status.Phase)
	}
	if got.Status.SearchAttempts.Count != 1 {
		t.Errorf("searchAttempts.count = %d, want 1", got.Status.SearchAttempts.Count)
	}

	// The grab stage keeps counting; the controller's fields survive.
	grab = catalogac.Movie(name, ns).WithStatus(
		catalogac.MovieStatus().
			WithLastSearchedAt(searched).
			WithSearchAttempts(commonv1alpha1.Attempts{Count: 2}),
	)
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogGrab, grab); err != nil {
		t.Fatalf("second grab PatchStatus: %v", err)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != catalogv1alpha1.MoviePhaseWanted || got.Status.Path != "/data/media/movies/Inception (2010)" {
		t.Errorf("controller fields lost after a second grab apply: phase=%q path=%q",
			got.Status.Phase, got.Status.Path)
	}
	if got.Status.SearchAttempts.Count != 2 {
		t.Errorf("searchAttempts.count = %d, want 2", got.Status.SearchAttempts.Count)
	}

	for _, want := range []string{"catalog", "catalog-grab"} {
		if !managesField(got.ManagedFields, want, "status") {
			t.Errorf("no %q status entry in managedFields: %+v", want, fieldManagerNames(got.ManagedFields))
		}
	}
}

// TestPatchStatusDropsFieldsItStopsSendingIsOwnershipScoped checks the other
// half of apply semantics: a manager that stops sending a field it owns
// releases it, but only its own.
func TestPatchStatusReleasesItsOwnFieldsOnly(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "release", "inception-abc1234567"

	newMovie(t, ctx, c, ns, name)

	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogGrab,
		catalogac.Movie(name, ns).WithStatus(
			catalogac.MovieStatus().WithSearchAttempts(commonv1alpha1.Attempts{Count: 1}).WithLastSearchedAt(metav1.Now()),
		)); err != nil {
		t.Fatalf("grab PatchStatus: %v", err)
	}
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalog,
		catalogac.Movie(name, ns).WithStatus(
			catalogac.MovieStatus().WithPhase(catalogv1alpha1.MoviePhaseWanted),
		)); err != nil {
		t.Fatalf("controller PatchStatus: %v", err)
	}

	// The grab stage stops reporting lastSearchedAt.
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogGrab,
		catalogac.Movie(name, ns).WithStatus(
			catalogac.MovieStatus().WithSearchAttempts(commonv1alpha1.Attempts{Count: 2}),
		)); err != nil {
		t.Fatalf("second grab PatchStatus: %v", err)
	}

	var got catalogv1alpha1.Movie
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.LastSearchedAt != nil {
		t.Errorf("lastSearchedAt = %v, want nil after the grab stage stopped sending it", got.Status.LastSearchedAt)
	}
	if got.Status.Phase != catalogv1alpha1.MoviePhaseWanted {
		t.Errorf("the controller's phase was dropped along with the grab stage's field: %q", got.Status.Phase)
	}
}

func TestPatchStatusRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "badinput", "inception-abc1234567"

	newMovie(t, ctx, c, ns, name)

	good := catalogac.Movie(name, ns).WithStatus(catalogac.MovieStatus().WithPhase(catalogv1alpha1.MoviePhaseWanted))

	if _, err := k8s.PatchStatus(ctx, c, k8s.FieldManager("kubectl"), good); err == nil {
		t.Error("an unlisted field manager was accepted")
	}
	if _, err := k8s.PatchStatus[*catalogac.MovieApplyConfiguration](
		ctx, c, k8s.ManagerCatalog, nil); err == nil {
		t.Error("a nil apply configuration was accepted")
	}
	if _, err := k8s.PatchStatus(ctx, nil, k8s.ManagerCatalog, good); err == nil {
		t.Error("a nil client was accepted")
	}

	// A configuration built by hand rather than through the generated
	// constructor has no name, and would otherwise be sent to the collection
	// endpoint.
	nameless := &catalogac.MovieApplyConfiguration{}
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalog, nameless); err == nil {
		t.Error("a nameless apply configuration was accepted")
	}
}

func managesField(entries []metav1.ManagedFieldsEntry, manager, subresource string) bool {
	for _, e := range entries {
		if e.Manager == manager && e.Subresource == subresource {
			return true
		}
	}
	return false
}

func fieldManagerNames(entries []metav1.ManagedFieldsEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Manager+"/"+e.Subresource)
	}
	return out
}
