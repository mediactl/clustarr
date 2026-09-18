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

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
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

func newDownload(t *testing.T, ctx context.Context, c client.Client, ns, name string) *downloadv1alpha1.Download {
	t.Helper()

	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil && !isAlreadyExists(err) {
		t.Fatalf("create namespace: %v", err)
	}

	magnet := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"
	d := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: downloadv1alpha1.DownloadSpec{
			Protocol: commonv1alpha1.ProtocolTorrent,
			Source:   downloadv1alpha1.DownloadSource{MagnetURL: &magnet},
			Release: commonv1alpha1.ReleaseInfo{
				GUID:        "https://indexer.example/details/1",
				IndexerRef:  "example",
				IndexerName: "Example",
				Title:       "Inception.2010.2160p.UHD.BluRay.x265-GROUP",
				Protocol:    commonv1alpha1.ProtocolTorrent,
				InfoHash:    "0123456789abcdef0123456789abcdef01234567",
			},
			Target: commonv1alpha1.MediaRef{
				Kind: commonv1alpha1.MediaKindMovie,
				Name: "inception",
			},
		},
	}
	if err := c.Create(ctx, d); err != nil {
		t.Fatalf("create Download: %v", err)
	}
	return d
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

	newDownload(t, ctx, c, ns, name)

	ac := downloadac.Download(name, ns).WithStatus(
		downloadac.DownloadStatus().
			WithPhase("Assigned").
			WithEngine("qbit-0").
			WithObservedGeneration(1),
	)
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr, ac); err != nil {
		t.Fatalf("PatchStatus: %v", err)
	}

	var got downloadv1alpha1.Download
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != "Assigned" {
		t.Errorf("phase = %q, want Assigned", got.Status.Phase)
	}
	if got.Status.Engine != "qbit-0" {
		t.Errorf("engine = %q, want qbit-0", got.Status.Engine)
	}
	if got.Spec.Release.Title == "" {
		t.Error("the spec was cleared by a status apply")
	}

	if !managesField(got.ManagedFields, "grabarr", "status") {
		t.Errorf("no grabarr-owned status entry in managedFields: %+v", fieldManagerNames(got.ManagedFields))
	}
}

// TestPatchStatusTwoManagersDoNotClobber is §3's single-writer rule as the
// apiserver enforces it: grabarr owns phase and engine, grabarr-engine owns
// the telemetry, and each may re-apply its own fields with force-ownership
// without erasing the other's.
func TestPatchStatusTwoManagersDoNotClobber(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "noclobber", "inception-abc1234567"

	newDownload(t, ctx, c, ns, name)

	controller := downloadac.Download(name, ns).WithStatus(
		downloadac.DownloadStatus().WithPhase("Downloading").WithEngine("qbit-0"),
	)
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr, controller); err != nil {
		t.Fatalf("controller PatchStatus: %v", err)
	}

	engine := downloadac.Download(name, ns).WithStatus(
		downloadac.DownloadStatus().
			WithDownloadedBytes(1024).
			WithDownloadRateBps(512).
			WithProgressPercent(10),
	)
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarrEngine, engine); err != nil {
		t.Fatalf("engine PatchStatus: %v", err)
	}

	var got downloadv1alpha1.Download
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != "Downloading" {
		t.Errorf("the engine's apply erased the controller's phase: %q", got.Status.Phase)
	}
	if got.Status.DownloadedBytes != 1024 {
		t.Errorf("downloadedBytes = %d, want 1024", got.Status.DownloadedBytes)
	}

	// The engine keeps reporting telemetry; the controller's fields survive.
	engine = downloadac.Download(name, ns).WithStatus(
		downloadac.DownloadStatus().
			WithDownloadedBytes(2048).
			WithDownloadRateBps(512).
			WithProgressPercent(20),
	)
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarrEngine, engine); err != nil {
		t.Fatalf("second engine PatchStatus: %v", err)
	}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Phase != "Downloading" || got.Status.Engine != "qbit-0" {
		t.Errorf("controller fields lost after a second engine apply: phase=%q engine=%q",
			got.Status.Phase, got.Status.Engine)
	}
	if got.Status.DownloadedBytes != 2048 {
		t.Errorf("downloadedBytes = %d, want 2048", got.Status.DownloadedBytes)
	}

	for _, want := range []string{"grabarr", "grabarr-engine"} {
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

	newDownload(t, ctx, c, ns, name)

	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarrEngine,
		downloadac.Download(name, ns).WithStatus(
			downloadac.DownloadStatus().WithDownloadedBytes(1024).WithSeeders(5),
		)); err != nil {
		t.Fatalf("engine PatchStatus: %v", err)
	}
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr,
		downloadac.Download(name, ns).WithStatus(
			downloadac.DownloadStatus().WithPhase("Downloading"),
		)); err != nil {
		t.Fatalf("controller PatchStatus: %v", err)
	}

	// The engine stops reporting seeders.
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarrEngine,
		downloadac.Download(name, ns).WithStatus(
			downloadac.DownloadStatus().WithDownloadedBytes(2048),
		)); err != nil {
		t.Fatalf("second engine PatchStatus: %v", err)
	}

	var got downloadv1alpha1.Download
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Seeders != 0 {
		t.Errorf("seeders = %d, want 0 after the engine stopped sending it", got.Status.Seeders)
	}
	if got.Status.Phase != "Downloading" {
		t.Errorf("the controller's phase was dropped along with the engine's field: %q", got.Status.Phase)
	}
}

func TestPatchStatusRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "badinput", "inception-abc1234567"

	newDownload(t, ctx, c, ns, name)

	good := downloadac.Download(name, ns).WithStatus(downloadac.DownloadStatus().WithPhase("Assigned"))

	if _, err := k8s.PatchStatus(ctx, c, k8s.FieldManager("kubectl"), good); err == nil {
		t.Error("an unlisted field manager was accepted")
	}
	if _, err := k8s.PatchStatus[*downloadac.DownloadApplyConfiguration](
		ctx, c, k8s.ManagerGrabarr, nil); err == nil {
		t.Error("a nil apply configuration was accepted")
	}
	if _, err := k8s.PatchStatus(ctx, nil, k8s.ManagerGrabarr, good); err == nil {
		t.Error("a nil client was accepted")
	}

	// A configuration built by hand rather than through the generated
	// constructor has no name, and would otherwise be sent to the collection
	// endpoint.
	nameless := &downloadac.DownloadApplyConfiguration{}
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr, nameless); err == nil {
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
