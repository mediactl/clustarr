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

package metadataprovider_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/metadataprovider"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
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

// namedVolumeServer answers any request with a fixed ComicVine
// search-volumes body naming exactly one result, so a test can tell which of
// two same-Name("comicvine") providers it actually talked to.
func namedVolumeServer(t *testing.T, name string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status_code": 1,
			"results":     []map[string]any{{"id": 1, "name": name}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBuildRegistryOrdersByPriorityAndSkipsDisabled(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := "registry-test"
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	if err := c.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "cv-creds", Namespace: ns},
		Data:       map[string][]byte{"apiKey": []byte("k")},
	}); err != nil {
		t.Fatalf("create secret: %v", err)
	}

	// Two comicvine providers with distinguishable backends, used to prove
	// BuildRegistry actually sorts by ascending Priority rather than
	// creation/list order: metadata.Provider exposes no per-instance
	// identity beyond Name() (checked with `go doc ./pkg/metadata Registry`
	// and `go doc ./pkg/metadata Provider`), so a Name() comparison alone
	// cannot distinguish "the Priority=10 one" from "the Priority=90 one".
	//
	// comicvine, not tmdb, carries this disambiguation: golang-tmdb (vendored
	// by pkg/metadata/clients/tmdb, outside this task's path ownership)
	// stores its base URL in a PACKAGE-LEVEL global variable (`var baseURL`
	// in tmdb.go, mutated by SetCustomBaseURL), not per-*Client -- verified
	// by reading the vendored source at
	// $(go env GOMODCACHE)/github.com/cyruzin/golang-tmdb@v1.9.4/tmdb.go.
	// Constructing two tmdb.Client instances with two different BaseURLs in
	// the same process makes both instances silently share whichever
	// baseURL was set last, so a network-based ordering check through two
	// live tmdb.Client values is unreliable and would flake or mis-assert
	// depending on construction order -- a real latent defect worth flagging
	// upstream, not a bug in this task's registry.go. comicvine (an in-house,
	// net/http-direct client with baseURL as a struct field, confirmed by
	// reading pkg/metadata/clients/comicvine/comicvine.go) has no such
	// issue.
	lowSrv := namedVolumeServer(t, "low-priority-backend")
	highSrv := namedVolumeServer(t, "high-priority-backend")

	providers := []catalogv1alpha1.MetadataProvider{
		{ObjectMeta: metav1.ObjectMeta{Name: "cv-low", Namespace: ns}, Spec: catalogv1alpha1.MetadataProviderSpec{
			Type: catalogv1alpha1.MetadataProviderComicVine, Priority: 90, BaseURL: &lowSrv.URL,
			SecretRef: &corev1.LocalObjectReference{Name: "cv-creds"},
		}},
		{ObjectMeta: metav1.ObjectMeta{Name: "cv-high", Namespace: ns}, Spec: catalogv1alpha1.MetadataProviderSpec{
			Type: catalogv1alpha1.MetadataProviderComicVine, Priority: 10, BaseURL: &highSrv.URL,
			SecretRef: &corev1.LocalObjectReference{Name: "cv-creds"},
		}},
		{ObjectMeta: metav1.ObjectMeta{Name: "disabled-cv", Namespace: ns}, Spec: catalogv1alpha1.MetadataProviderSpec{
			Type: catalogv1alpha1.MetadataProviderComicVine, Enabled: boolPtr(false),
			SecretRef: &corev1.LocalObjectReference{Name: "cv-creds"},
		}},
		{ObjectMeta: metav1.ObjectMeta{Name: "coverart", Namespace: ns}, Spec: catalogv1alpha1.MetadataProviderSpec{
			Type: catalogv1alpha1.MetadataProviderCoverArt,
		}},
	}
	for i := range providers {
		if err := c.Create(ctx, &providers[i]); err != nil {
			t.Fatalf("create %s: %v", providers[i].Name, err)
		}
	}

	reg, err := metadataprovider.BuildRegistry(ctx, c, ns, nil)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if len(reg.Comics) != 2 {
		t.Fatalf("got %d comic providers, want 2 (the disabled one excluded; coverart is not a comic provider)", len(reg.Comics))
	}
	if len(reg.Artwork) != 1 || reg.Artwork[0].Name() != "coverart" {
		t.Fatalf("got artwork providers %v, want the coverart provider (X6b: no type is left without a client)", reg.Artwork)
	}
	if reg.Comics[0].Name() != "comicvine" || reg.Comics[1].Name() != "comicvine" {
		t.Errorf("both entries should be comicvine clients (only Priority differs), got %s / %s", reg.Comics[0].Name(), reg.Comics[1].Name())
	}

	first, err := reg.Comics[0].SearchVolumes(ctx, "batman")
	if err != nil {
		t.Fatalf("Comics[0].SearchVolumes: %v", err)
	}
	if len(first) != 1 || first[0].Title != "high-priority-backend" {
		t.Errorf("Comics[0] talked to %+v, want the Priority=10 provider's backend (high-priority-backend)", first)
	}

	second, err := reg.Comics[1].SearchVolumes(ctx, "batman")
	if err != nil {
		t.Fatalf("Comics[1].SearchVolumes: %v", err)
	}
	if len(second) != 1 || second[0].Title != "low-priority-backend" {
		t.Errorf("Comics[1] talked to %+v, want the Priority=90 provider's backend (low-priority-backend)", second)
	}
}

func boolPtr(b bool) *bool { return &b }
