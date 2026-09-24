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
// The transcode leg of "the KEDA opt-in renders" is gone: the 2026-09-23
// transcode-worker-pools design retired the per-task KEDA ScaledJob example
// (config/keda/transcode-scaledjob.yaml) in favour of the pool Jobs
// squasharr's own controllers size and suspend, so there is nothing left
// here for this file to render and assert against. config/keda now carries
// only captionarr-worker's ScaledObject, which is scenario 13's concern
// (test/e2e/subtitle_test.go), not this file's.
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
	"testing"
)

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
