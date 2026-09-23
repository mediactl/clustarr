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

// X12c (docs/superpowers/plans/2026-09-23-gap-fixes.md) -- Phase H scenario
// 16 (remaining-work.md): "The Helm chart with e2e values passes scenario 1
// as the kustomize overlay does; clustarr all in one pod passes scenario 1;
// the KEDA opt-in renders."
//
// # What this file does and does not prove
//
// TestKEDATranscodeScaledJobRenders is the one leg this task can actually
// build and prove: a static `kustomize build config/keda` render, parsed
// and asserted against the two fields the carried-defect text names.
// config/keda/** is squasharr's own owned path (Task X10, "config/keda:
// squasharr-worker SA and --job" -- docs/superpowers/plans/2026-09-23-
// gap-fixes.md), so this file only reads its rendered output; it never
// edits config/keda itself. This test is written to PASS once that fix
// lands, and to fail loudly, naming the exact field, until it does -- a
// real regression guard, not a description of a known-broken state.
//
// The Helm-chart and clustarr-all legs are named here as SKIPPED, not
// implemented, for a structural reason rather than an oversight:
// TestMain's own gate (main_test.go's package doc comment) requires 29
// CRDs, NATS and every Deployment in deploymentRoster to ALREADY be
// Available before ANY test in this package runs -- "a scenario never
// installs anything itself". Proving "the Helm chart passes scenario 1"
// or "clustarr all in one pod passes scenario 1" needs the CLUSTER ITSELF
// stood up from a different set of manifests (helm install, or a
// single-Deployment "clustarr all" workload with a union ServiceAccount/
// ClusterRole) BEFORE that gate runs -- which is a second, alternate
// hack/e2e.sh-shaped invocation with a different deploy step, not
// something any Go test function in this already-gated package can do
// after the fact. Building that second deploy path also crosses two
// ownership lines this task does not: charts/clustarr/** is Task X12a's
// ("chart and deploy config"), and a working "clustarr all" Deployment
// needs a ServiceAccount bound to the union of every service's
// ClusterRole, which is RBAC generation -- explicitly W2-only per every
// W1 task's dispatch contract ("Nobody but W2 edits cmd/clustarr/**,
// config/rbac/**, chart RBAC templates"). Both are named in this task's
// report under "Needs from others" rather than worked around here.
package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

// kustomizeBuild shells out to `kustomize build dir` (the same binary
// hack/e2e.sh's own KUSTOMIZE variable requires) and returns every
// document it renders as an unstructured.Unstructured, in order. It needs
// no live cluster -- this is a pure static render -- but lives in this
// package (behind TestMain's cluster gate) anyway, alongside every other
// scenario, rather than as a plain non-e2e unit test: it verifies exactly
// the deploy-time config this task's brief scopes to test/e2e and
// config/e2e, not a library.
func kustomizeBuild(t *testing.T, dir string) []unstructured.Unstructured {
	t.Helper()
	kustomizePath, err := exec.LookPath("kustomize")
	require.NoErrorf(t, err, "kustomize not on PATH -- hack/e2e.sh requires it too")

	out, err := exec.Command(kustomizePath, "build", dir).CombinedOutput()
	require.NoErrorf(t, err, "kustomize build %s: %s", dir, out)

	var docs []unstructured.Unstructured
	reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(out)))
	for {
		raw, rerr := reader.Read()
		if len(strings.TrimSpace(string(raw))) > 0 {
			jsonBytes, jerr := yaml.YAMLToJSON(raw)
			require.NoErrorf(t, jerr, "convert one %s document to JSON", dir)
			var u unstructured.Unstructured
			require.NoErrorf(t, json.Unmarshal(jsonBytes, &u.Object), "unmarshal one %s document", dir)
			if u.Object != nil {
				docs = append(docs, u)
			}
		}
		if rerr != nil {
			break
		}
	}
	require.NotEmptyf(t, docs, "kustomize build %s rendered no documents", dir)
	return docs
}

// TestKEDATranscodeScaledJobRenders is scenario 16's KEDA leg. See this
// file's own package doc comment for what it does and does not prove.
//
// The carried defect (remaining-work.md, "Carried out of Phases E, F and
// G"): "config/keda/transcode-scaledjob.yaml ... cannot work as written:
// it runs as the squasharr ServiceAccount instead of squasharr-worker,
// and starts the worker without --job, which squasharr's option
// validation rejects." Both fields are asserted directly below, read off
// the rendered ScaledJob rather than the source YAML, so a kustomize
// patch elsewhere in config/keda that changes the effective value would
// still be caught.
func TestKEDATranscodeScaledJobRenders(t *testing.T) {
	docs := kustomizeBuild(t, "../../config/keda")

	var scaledJob *unstructured.Unstructured
	for i := range docs {
		if docs[i].GetKind() == "ScaledJob" && docs[i].GetName() == "squasharr-transcode" {
			scaledJob = &docs[i]
			break
		}
	}
	require.NotNilf(t, scaledJob, "kustomize build config/keda rendered no ScaledJob/squasharr-transcode")

	sa, found, err := unstructured.NestedString(scaledJob.Object,
		"spec", "jobTargetRef", "template", "spec", "serviceAccountName")
	require.NoError(t, err)
	require.True(t, found, "ScaledJob/squasharr-transcode: spec.jobTargetRef.template.spec.serviceAccountName is not set")
	require.Equal(t, "squasharr-worker", sa,
		"ScaledJob/squasharr-transcode must run as the squasharr-worker ServiceAccount, matching the "+
			"batch/v1 Jobs squasharr's own TranscodeJob controller creates (config/manager/squasharr.yaml), "+
			"not the controller's own squasharr ServiceAccount")

	containers, found, err := unstructured.NestedSlice(scaledJob.Object,
		"spec", "jobTargetRef", "template", "spec", "containers")
	require.NoError(t, err)
	require.True(t, found, "ScaledJob/squasharr-transcode: spec.jobTargetRef.template.spec.containers is not set")
	require.NotEmpty(t, containers)

	container, ok := containers[0].(map[string]interface{})
	require.True(t, ok)
	argsRaw, found, err := unstructured.NestedStringSlice(container, "args")
	require.NoError(t, err)
	require.True(t, found, "ScaledJob/squasharr-transcode's worker container has no args")
	require.Containsf(t, argsRaw, "--job", "ScaledJob/squasharr-transcode's worker container args %v must "+
		"include --job -- squasharr's own option validation rejects `squasharr --role worker` started "+
		"without it (this is a batch Job, not the long-running controller Deployment)", argsRaw)
}

// TestHelmChartE2EValuesPassesScenarioOne documents, rather than runs,
// scenario 16's Helm-chart leg. See this file's own package doc comment
// for the structural reason (TestMain's cluster-already-up gate) and the
// ownership lines (charts/clustarr/** is Task X12a's) that put building it
// out of this task's reach.
func TestHelmChartE2EValuesPassesScenarioOne(t *testing.T) {
	t.Skip("needs a SEPARATE cluster stood up from charts/clustarr (helm install/template) before " +
		"TestMain's gate would even let this package's tests run -- no test/e2e function can do that " +
		"after the fact. Needs: an e2e values file for charts/clustarr (Task X12a's own path) and a " +
		"hack/e2e.sh mode (or sibling script) that installs the chart instead of `kustomize build " +
		"config/e2e | kubectl apply` before re-running this suite's existing scenario 1 " +
		"(TestDownloadTorrentGrabToImportAttempt) against it -- see this file's package doc comment.")
}

// TestClustarrAllSinglePodPassesScenarioOne documents, rather than runs,
// scenario 16's `clustarr all` leg, for the identical structural reason as
// TestHelmChartE2EValuesPassesScenarioOne, plus one more: a working
// all-in-one Deployment needs a ServiceAccount bound to the UNION of every
// service's ClusterRole, which is RBAC generation -- explicitly W2-only
// per every W1 task's dispatch contract, not something config/e2e (this
// task's own path) may add on its own.
func TestClustarrAllSinglePodPassesScenarioOne(t *testing.T) {
	t.Skip("needs a SEPARATE cluster stood up with a single `clustarr all` Deployment (a union " +
		"ServiceAccount/ClusterRole across every service -- RBAC generation, W2-only per every W1 task's " +
		"dispatch contract) before TestMain's gate would even let this package's tests run -- see this " +
		"file's package doc comment. Needs: a config/e2e-adjacent manifest running `clustarr all` and " +
		"the RBAC union it needs, from whichever task owns config/rbac and cmd/clustarr wiring.")
}
