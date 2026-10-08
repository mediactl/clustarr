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

package admission_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

var (
	testCfg    *rest.Config
	testClient client.Client
)

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run()) // the envtests skip; the pure tests run
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start envtest: %v\n", err)
		os.Exit(1)
	}
	code := func() int {
		c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
		if err != nil {
			fmt.Fprintf(os.Stderr, "build client: %v\n", err)
			return 1
		}
		testCfg, testClient = cfg, c
		return m.Run()
	}()
	if err := env.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stop envtest: %v\n", err)
	}
	os.Exit(code)
}

// requireEnvtest is the envtest client, in a namespace of the test's own.
func requireEnvtest(t *testing.T, ns string) client.Client {
	t.Helper()
	if testClient == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	require.NoError(t, testClient.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	return testClient
}

// seed creates a movie MediaFile and writes its status as the loop does: the
// whole status, through the apply configuration, as one field manager.
func seed(t *testing.T, c client.Client, ns, name string, st catalogv1alpha1.MediaFileStatus) *catalogv1alpha1.MediaFile {
	t.Helper()
	ctx := context.Background()
	mf := &catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: catalogv1alpha1.MediaFileSpec{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name},
			Path: "/data/m/" + name + ".mkv", Quality: commonv1.Quality{Name: "Bluray-1080p"}}}
	require.NoError(t, c.Create(ctx, mf))
	raw, err := json.Marshal(st)
	require.NoError(t, err)
	ac := &catalogac.MediaFileStatusApplyConfiguration{}
	require.NoError(t, json.Unmarshal(raw, ac))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalog, catalogac.MediaFile(name, ns).WithStatus(ac))
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(mf), mf))
	return mf
}

// block is a status whose transcode block is phase at lastSeq seq; a
// Queued or Running one carries its class, pool and dispatch.
func block(phase catalogv1alpha1.TranscodePhase, seq int64, class string) catalogv1alpha1.MediaFileStatus {
	b := &catalogv1alpha1.TranscodeState{Phase: phase, Profile: "hevc", ProfileHash: "h", ProbeHash: "p",
		PlannedAt: new(metav1.NewTime(time.Now().UTC().Truncate(time.Second)))}
	if phase == catalogv1alpha1.TranscodePhaseQueued || phase == catalogv1alpha1.TranscodePhaseRunning {
		b.Class, b.Pool = catalogv1alpha1.TranscodeClass(class), "pool-"+class
		b.Dispatch = &catalogv1alpha1.Dispatch{Seq: seq, DispatchedAt: metav1.NewTime(time.Now().UTC().Truncate(time.Second))}
	}
	return catalogv1alpha1.MediaFileStatus{ProbeHash: "p", LastSeq: seq, Transcode: b}
}

// emptyReader is a cache that has caught up with nothing.
type emptyReader struct{ client.Reader }

func (emptyReader) List(context.Context, client.ObjectList, ...client.ListOption) error { return nil }
