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

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	"github.com/mediactl/clustarr/captionarr"
	"github.com/mediactl/clustarr/catalogarr"
	"github.com/mediactl/clustarr/grabarr"
	"github.com/mediactl/clustarr/importarr"
	"github.com/mediactl/clustarr/indexarr"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/squasharr"
	"github.com/mediactl/clustarr/ui"
)

// validatable is what every service's Options satisfies, so one table can
// execute a command line and check the options it produced.
type validatable interface{ Validate() error }

// topology is §3's process-topology table, for the Deployments config/manager
// declares. The engine StatefulSets and the transcode Jobs are absent on
// purpose: grabarr and squasharr create those at runtime, owned by the CR that
// asked for them, so no manifest can declare them.
var topology = map[string]struct {
	replicas int32
	strategy appsv1.DeploymentStrategyType
	// leaderElect is true for a Deployment that reconciles CRs, which §3 and
	// §12 run under a lease. Roles that only consume queue work must not
	// wait for one.
	leaderElect bool
}{
	"catalogarr":          {replicas: 1, leaderElect: true},
	"catalogarr-metadata": {replicas: 1, strategy: appsv1.RecreateDeploymentStrategyType},
	"indexarr":            {replicas: 1, strategy: appsv1.RecreateDeploymentStrategyType},
	"grabarr":             {replicas: 1, leaderElect: true},
	"squasharr":           {replicas: 1, leaderElect: true},
	"captionarr":          {replicas: 1, leaderElect: true},
	"captionarr-worker":   {replicas: 2},
	"importarr":           {replicas: 1, leaderElect: true},
	"importarr-worker":    {replicas: 2},
	// ui has no --leader-elect and no --role: it runs no controller-runtime
	// manager and reconciles nothing (amendment §A3).
	"ui": {replicas: 1},
}

// TestManagerManifestsMatchTheCLI reads every Deployment in config/manager and
// runs its container argv through the real command tree.
//
// This is the seam the manifests and the binary meet at, and nothing else
// watches it: a `--role` value or a flag name the CLI does not accept is a
// crash loop in a cluster and a green build everywhere else. Reading the YAML
// rather than restating it means the test cannot drift from what ships.
func TestManagerManifestsMatchTheCLI(t *testing.T) {
	// The pods get these from their Deployment rather than argv, and the
	// options are only valid with them.
	t.Setenv(natsURLEnv, "nats://nats.clustarr-system.svc:4222")
	t.Setenv(indexPathEnv, "/var/lib/clustarr/index/releases.db")
	t.Setenv(namespaceEnv, "clustarr-system")

	paths, err := filepath.Glob("../../config/manager/*.yaml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no manifests under config/manager: %v", err)
	}

	seen := map[string]bool{}
	for _, path := range paths {
		for _, d := range deploymentsIn(t, path) {
			name := d.Name
			want, known := topology[name]
			if !known {
				t.Errorf("%s declares Deployment %q, which is not a row of §3's topology table", path, name)
				continue
			}
			seen[name] = true

			replicas := int32(1) // the apps/v1 default when the field is unset
			if d.Spec.Replicas != nil {
				replicas = *d.Spec.Replicas
			}
			if replicas != want.replicas {
				t.Errorf("Deployment %q has %d replicas, want %d (§3)", name, replicas, want.replicas)
			}
			if got := d.Spec.Strategy.Type; got != want.strategy {
				t.Errorf("Deployment %q strategy = %q, want %q (§3)", name, got, want.strategy)
			}

			if n := len(d.Spec.Template.Spec.Containers); n != 1 {
				t.Errorf("Deployment %q has %d containers, want 1", name, n)
				continue
			}
			argv := d.Spec.Template.Spec.Containers[0].Args
			if len(argv) == 0 {
				t.Errorf("Deployment %q passes no args", name)
				continue
			}

			t.Run(name, func(t *testing.T) {
				got := stubEveryService(t)
				if out, err := execute(t, argv...); err != nil {
					t.Fatalf("clustarr %v: %v (output %q)", argv, err, out)
				}
				if *got == nil {
					t.Fatalf("clustarr %v ran no service", argv)
				}
				if err := (*got).Validate(); err != nil {
					t.Fatalf("clustarr %v produced invalid options: %v", argv, err)
				}
				if leaderElect(argv) != want.leaderElect {
					t.Errorf("Deployment %q %s --leader-elect; §3 says it should%s be leader-elected",
						name, presence(leaderElect(argv)), negation(want.leaderElect))
				}
			})
		}
	}

	for name := range topology {
		if !seen[name] {
			t.Errorf("§3's topology table has a %q Deployment and config/manager does not", name)
		}
	}
}

func presence(b bool) string {
	if b {
		return "passes"
	}
	return "does not pass"
}

func negation(b bool) string {
	if b {
		return ""
	}
	return " not"
}

func leaderElect(argv []string) bool {
	for _, a := range argv {
		if a == "--leader-elect" || a == "--leader-elect=true" {
			return true
		}
	}
	return false
}

// deploymentsIn decodes every Deployment document in a manifest file.
func deploymentsIn(t *testing.T, path string) []appsv1.Deployment {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Errorf("close %s: %v", path, err)
		}
	}()

	var out []appsv1.Deployment
	dec := utilyaml.NewYAMLOrJSONDecoder(f, 4096)
	for {
		var d appsv1.Deployment
		switch err := dec.Decode(&d); {
		case errors.Is(err, io.EOF):
			return out
		case err != nil:
			t.Fatalf("decode %s: %v", path, err)
		}
		if d.Kind == "Deployment" {
			out = append(out, d)
		}
	}
}

// stubEveryService replaces every entrypoint with a recorder, so a caller can
// execute an arbitrary argv without knowing which service it names.
func stubEveryService(t *testing.T) *validatable {
	t.Helper()
	var got validatable

	catalog, index, grab, squash, caption, importa, uiRun :=
		runCatalogarr, runIndexarr, runGrabarr, runSquasharr, runCaptionarr, runImportarr, runUI
	t.Cleanup(func() {
		runCatalogarr, runIndexarr, runGrabarr, runSquasharr, runCaptionarr, runImportarr, runUI =
			catalog, index, grab, squash, caption, importa, uiRun
	})

	runCatalogarr = func(_ context.Context, o catalogarr.Options) error { got = o; return nil }
	runIndexarr = func(_ context.Context, o indexarr.Options) error { got = o; return nil }
	runGrabarr = func(_ context.Context, o grabarr.Options) error { got = o; return nil }
	runSquasharr = func(_ context.Context, o squasharr.Options) error { got = o; return nil }
	runCaptionarr = func(_ context.Context, o captionarr.Options) error { got = o; return nil }
	runImportarr = func(_ context.Context, o importarr.Options) error { got = o; return nil }
	runUI = func(_ context.Context, o ui.Options) error { got = o; return nil }
	return &got
}

// TestCatalogarrRoleCombinations pins the semantics §3 needs: one Deployment
// runs the controllers, the queue workers and the history sink, and it must
// NOT be `--role all`, which would start a second metadata gateway beside the
// single-replica catalogarr-metadata Deployment.
func TestCatalogarrRoleCombinations(t *testing.T) {
	valid := []struct {
		role        catalogarr.Role
		controllers bool
		workers     bool
	}{
		{"controller", true, false},
		{"worker", false, true},
		{"metadata", false, true},
		{"history", false, true},
		{"all", true, true},
		{"controller,worker,history", true, true},
		{"worker,history", false, true},
		{"controller, worker", true, true},
	}
	for _, tc := range valid {
		if !tc.role.Valid() {
			t.Errorf("role %q is rejected", tc.role)
			continue
		}
		if got := tc.role.RunsControllers(); got != tc.controllers {
			t.Errorf("role %q RunsControllers = %v, want %v", tc.role, got, tc.controllers)
		}
		if got := tc.role.RunsWorkers(); got != tc.workers {
			t.Errorf("role %q RunsWorkers = %v, want %v", tc.role, got, tc.workers)
		}
	}

	for _, role := range []catalogarr.Role{"", ",", "bogus", "controller,bogus", "controller,controller", "all,,"} {
		if role.Valid() {
			t.Errorf("role %q is accepted", role)
		}
	}

	// The catalogarr Deployment runs the queue workers but not the metadata
	// gateway, so its role must not name metadata.
	const deployed catalogarr.Role = "controller,worker,history"
	if deployed.Has(catalogarr.RoleMetadata) {
		t.Error("the catalogarr Deployment's role would start a second metadata gateway")
	}
}

// TestEnvironmentSuppliesDefaults proves the variables the Deployments set are
// actually read. A flag default that ignored them would point every pod at
// nats://clustarr-nats:4222 and indexarr at an /index directory that is not
// mounted.
func TestEnvironmentSuppliesDefaults(t *testing.T) {
	t.Setenv(natsURLEnv, "nats://nats.clustarr-system.svc:4222")
	t.Setenv(indexPathEnv, "/var/lib/clustarr/index/releases.db")

	catalog := stub(t, &runCatalogarr)
	if _, err := execute(t, "catalogarr", "--role", "controller"); err != nil {
		t.Fatalf("clustarr catalogarr: %v", err)
	}
	if catalog.NATSURL != "nats://nats.clustarr-system.svc:4222" {
		t.Errorf("NATSURL = %q, want the value of $%s", catalog.NATSURL, natsURLEnv)
	}

	index := stub(t, &runIndexarr)
	if _, err := execute(t, "indexarr"); err != nil {
		t.Fatalf("clustarr indexarr: %v", err)
	}
	if index.IndexPath != "/var/lib/clustarr/index/releases.db" {
		t.Errorf("IndexPath = %q, want the value of $%s", index.IndexPath, indexPathEnv)
	}

	// An explicit flag still wins over the environment.
	index = stub(t, &runIndexarr)
	if _, err := execute(t, "indexarr", "--index-path", "/elsewhere/releases.db"); err != nil {
		t.Fatalf("clustarr indexarr: %v", err)
	}
	if index.IndexPath != "/elsewhere/releases.db" {
		t.Errorf("--index-path did not override $%s: %q", indexPathEnv, index.IndexPath)
	}
}

// TestWorkerGracePeriodsCoverAckWait pins a link that lives half in Go and
// half in YAML, and that nothing else can see: a worker Deployment's
// terminationGracePeriodSeconds must be at least the AckWait of every
// consumer it drains. Below that, SIGTERM kills the pod before it can finish
// or release an in-flight message, and the task sits invisible until AckWait
// expires.
//
// config/manager/captionarr-worker.yaml already documents the rule in a
// comment ("terminationGracePeriodSeconds is set to the JetStream AckWait");
// this is the check. importarr-worker is the reason it exists -- its three
// consumers (amendment §A1.6) were added with the manifest already written.
func TestWorkerGracePeriodsCoverAckWait(t *testing.T) {
	// Deployment name -> the consumers that Deployment's role drains.
	workers := map[string][]string{
		"importarr-worker": {
			events.ConsumerImportScan,
			events.ConsumerImportList,
			events.ConsumerImportFile,
		},
		"captionarr-worker": {
			events.ConsumerCaptionFetchHigh,
			events.ConsumerCaptionFetchNormal,
		},
	}

	top := events.Default()
	for name, consumers := range workers {
		path := filepath.Join("../../config/manager", name+".yaml")
		deployments := deploymentsIn(t, path)
		if len(deployments) != 1 {
			t.Fatalf("%s declares %d Deployments, want 1", path, len(deployments))
		}
		grace := deployments[0].Spec.Template.Spec.TerminationGracePeriodSeconds
		if grace == nil {
			t.Errorf("%s sets no terminationGracePeriodSeconds", path)
			continue
		}
		graceDur := time.Duration(*grace) * time.Second

		for _, cname := range consumers {
			c, ok := top.Consumer(cname)
			if !ok {
				t.Errorf("%s: consumer %q is not in the default topology", name, cname)
				continue
			}
			if c.AckWait > graceDur {
				t.Errorf("%s: consumer %s AckWait = %s exceeds terminationGracePeriodSeconds = %s in %s; "+
					"a SIGTERMed worker cannot release its message before the pod is killed",
					name, c.Name, c.AckWait, graceDur, path)
			}
		}
	}
}
