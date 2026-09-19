//go:build e2e

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

// Package e2e is the end-to-end suite: real CRs, real controllers, real NATS
// and real files on a real kind cluster's /data mount. It is the only thing
// that satisfies the project's standing rule that nothing is finished until
// it is proven end to end (CLAUDE.md; the remaining-work plan's Phase H).
//
// # How it reaches the cluster
//
// The test binary runs on the HOST, against the kind kubeconfig context. It
// can reach exactly two things: the API server, and the host directory kind
// mounts at /data. There are no extraPortMappings for NATS (4222/8222) or
// for any Service, so a scenario observes the system only through the
// Kubernetes API and through files under $CLUSTARR_DATA_DIR. Nothing here
// dials NATS or a fixture Service; NATS readiness is read from its
// StatefulSet's status, mirroring hack/kind.sh's own wait_for_nats.
//
// # What it refuses to do
//
// A scenario never installs anything. TestMain gates on the cluster already
// being complete -- 29 CRDs, a ready NATS StatefulSet, every Clustarr
// Deployment Available -- and exits non-zero with an explanation otherwise.
// That gate is structurally impossible to exercise under envtest, which runs
// no kube-controller-manager and therefore never populates
// Deployment.status.conditions (docs/research/k8s.md §8); hack/e2e.sh is what
// makes it true.
package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/test/fixtures/seed"
)

const (
	// Namespace is where every Clustarr Deployment and every CR this suite
	// creates lives; config/default declares exactly one namespace.
	Namespace = "clustarr-system"

	// KubeContext is the kind context hack/kind.sh creates.
	KubeContext = "kind-clustarr"

	// PartOfLabel selects everything config/crd, config/manager and
	// config/e2e label as belonging to Clustarr.
	PartOfLabel = "app.kubernetes.io/part-of"

	// PartOfValue is that label's value.
	PartOfValue = "clustarr"

	// QualityProfileName is the profile config/e2e/quality-profile.yaml
	// installs and every scenario's Movie/Series references.
	QualityProfileName = "e2e-any"

	// expectedCRDCount is config/crd/kustomization.yaml's 29 entries.
	expectedCRDCount = 29

	// gateTimeout bounds each individual readiness poll.
	gateTimeout = 5 * time.Minute

	// gatePollInterval is how often each readiness poll re-checks.
	gatePollInterval = 2 * time.Second
)

// k8sClient is built once in TestMain and only read afterwards.
var k8sClient client.Client

func TestMain(m *testing.M) {
	// gate() owns every deferred cleanup, because os.Exit skips defers:
	// TestMain itself does nothing but translate its verdict into an exit
	// code and, when the cluster is ready, hand off to m.Run.
	if code := gate(); code != 0 {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// gate builds the client and refuses to let any scenario run against an
// incomplete cluster. It returns 0 when the suite may proceed.
func gate() int {
	ctx, cancel := context.WithTimeout(context.Background(), 3*gateTimeout)
	defer cancel()

	cfg, err := ctrlconfig.GetConfigWithContext(KubeContext)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: kubeconfig context %q not reachable: %v\n", KubeContext, err)
		fmt.Fprintln(os.Stderr, "e2e: run hack/e2e.sh, not `go test` directly, unless a kind cluster named clustarr is already up")
		return 1
	}

	scheme := k8s.MustNewScheme()
	// pkg/k8s's scheme covers every group a MANAGER needs; the CRD-count
	// gate is the one thing only a test asks for, so apiextensions is added
	// here rather than widening the production scheme.
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: register apiextensions scheme: %v\n", err)
		return 1
	}

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build client: %v\n", err)
		return 1
	}
	k8sClient = c

	if err := checkDataDir(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: %v\n", err)
		fmt.Fprintln(os.Stderr, "e2e: hack/e2e.sh must export CLUSTARR_DATA_DIR and seed the fixture clip before `make e2e` runs")
		return 1
	}

	if err := waitReady(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: cluster not ready: %v\n", err)
		fmt.Fprintln(os.Stderr, "e2e: a scenario never installs anything itself -- hack/e2e.sh must finish CRDs, NATS and every Deployment before `make e2e` runs")
		return 1
	}

	return 0
}

// checkDataDir proves the host side of the hostPath mount is usable before
// any scenario plants a file into it, and that hack/e2e.sh seeded the probe
// clip. Both failures are setup mistakes with unhelpful symptoms hours later
// (an empty catalog, a scan that matched nothing), so they are caught here.
func checkDataDir() error {
	dir := dataDirOrEmpty()
	if dir == "" {
		return fmt.Errorf("CLUSTARR_DATA_DIR is unset")
	}
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("CLUSTARR_DATA_DIR %q is not usable: %w", dir, err)
	}
	clip := filepath.Join(dir, fixtureDirName, seed.ClipName)
	info, err := os.Stat(clip)
	if err != nil {
		return fmt.Errorf("the seeded probe clip %q is missing: %w", clip, err)
	}
	if info.Size() < seed.MinMediaBytes {
		return fmt.Errorf(
			"the seeded probe clip %q is %d bytes, under pkg/fsops's %d-byte sample threshold: "+
				"every planted file would be classified as a sample and skipped",
			clip, info.Size(), seed.MinMediaBytes)
	}
	return nil
}

// waitReady is the "TestMain refuses to run" gate: 29 CRDs installed, NATS's
// StatefulSet has a ready replica, and every Clustarr Deployment reports
// Available=True. It cannot be exercised under envtest (docs/research/k8s.md
// §8: no kube-controller-manager, so Deployment.status.conditions never
// populates) -- only against a real cluster.
func waitReady(ctx context.Context) error {
	if err := wait.PollUntilContextTimeout(ctx, gatePollInterval, gateTimeout, true, crdsInstalled); err != nil {
		return fmt.Errorf("CRDs: %w", err)
	}
	if err := wait.PollUntilContextTimeout(ctx, gatePollInterval, gateTimeout, true, natsReady); err != nil {
		return fmt.Errorf("NATS: %w", err)
	}
	if err := wait.PollUntilContextTimeout(ctx, gatePollInterval, gateTimeout, true, deploymentsAvailable); err != nil {
		return fmt.Errorf("Deployments: %w", err)
	}
	return nil
}

func crdsInstalled(ctx context.Context) (bool, error) {
	var list apiextensionsv1.CustomResourceDefinitionList
	if err := k8sClient.List(ctx, &list, client.MatchingLabels{PartOfLabel: PartOfValue}); err != nil {
		//nolint:nilerr // keep polling: the apiserver may still be settling
		return false, nil
	}
	return len(list.Items) == expectedCRDCount, nil
}

// natsReady mirrors hack/kind.sh's wait_for_nats (`rollout status
// statefulset/nats`) through the API rather than dialing 4222, which the
// test process cannot reach: kind publishes no port for it.
func natsReady(ctx context.Context) (bool, error) {
	var sts appsv1.StatefulSet
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: "nats"}, &sts); err != nil {
		//nolint:nilerr // keep polling
		return false, nil
	}
	return sts.Status.ReadyReplicas >= 1, nil
}

func deploymentsAvailable(ctx context.Context) (bool, error) {
	var list appsv1.DeploymentList
	if err := k8sClient.List(ctx, &list, client.InNamespace(Namespace), client.MatchingLabels{PartOfLabel: PartOfValue}); err != nil {
		//nolint:nilerr // keep polling
		return false, nil
	}
	if len(list.Items) == 0 {
		return false, nil
	}
	for i := range list.Items {
		if !deploymentAvailable(&list.Items[i]) {
			return false, nil
		}
	}
	return true, nil
}

func deploymentAvailable(d *appsv1.Deployment) bool {
	for _, c := range d.Status.Conditions {
		if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
