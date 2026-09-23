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
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	"github.com/mediactl/clustarr/squasharr"
	"github.com/mediactl/clustarr/squasharr/worker"
)

// runMainEnv, when set in this test binary's environment, makes the binary
// BE clustarr: TestMain runs main() with the JSON-encoded argv it carries
// instead of running tests. That is how a test observes the real process
// exit status -- os.Exit in main, not a value a function returned.
const runMainEnv = "CLUSTARR_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if raw := os.Getenv(runMainEnv); raw != "" {
		var argv []string
		if err := json.Unmarshal([]byte(raw), &argv); err != nil {
			panic(err)
		}
		os.Args = append([]string{"clustarr"}, argv...)
		main()
		os.Exit(0) // main returns only on success
	}
	os.Exit(m.Run())
}

// runClustarr runs this test binary as clustarr with args and exactly env,
// and returns its exit status and combined output.
func runClustarr(t *testing.T, env []string, args ...string) (int, string) {
	t.Helper()
	argv, err := json.Marshal(args)
	require.NoError(t, err)
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(append([]string(nil), env...), runMainEnv+"="+string(argv))
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), string(out)
	}
	require.NoError(t, err, "run clustarr %v: %s", args, out)
	return 0, string(out)
}

// unreachableKubeconfig is a well-formed kubeconfig naming a server nothing
// listens on: enough for ctrl.GetConfig and a lazy client, never contacted.
const unreachableKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster: {server: "https://127.0.0.1:1"}
contexts:
- name: c
  context: {cluster: c, user: u}
current-context: c
users:
- name: u
  user: {token: x}
`

// TestSquasharrWorkerExitCodeReachesTheProcess is Phase E ruling R4 at the
// only level it means anything: the exit status of the process the Job's
// kubelet watches.
//
// podFailurePolicy fails the Job on exit 3 (the source is not the planned
// file) and exit 4 (the output failed verification) and retries anything
// else. worker.Run classifies every failure correctly, but main exited 1 on
// any error -- so a 3 or a 4 became a 1, the Job retried, and a bad source
// was transcoded backoffLimit more times for the same answer. Asserting
// worker.Run's returned int cannot see that; only the process can.
func TestSquasharrWorkerExitCodeReachesTheProcess(t *testing.T) {
	tmp := t.TempDir()
	kubeconfig := filepath.Join(tmp, "kubeconfig")
	require.NoError(t, os.WriteFile(kubeconfig, []byte(unreachableKubeconfig), 0o600))
	emptyPath := filepath.Join(tmp, "empty-bin")
	require.NoError(t, os.Mkdir(emptyPath, 0o755))
	base := []string{"HOME=" + tmp, "KUBECONFIG=" + kubeconfig}

	t.Run("retriable: no ffmpeg on this node exits 2", func(t *testing.T) {
		code, out := runClustarr(t, append(base, "PATH="+emptyPath),
			"squasharr", "--role", "worker", "--job", "film-abc", "--namespace", "media", "--data-dir", tmp)
		require.Equal(t, worker.ExitRetriable, code, "output:\n%s", out)
		require.Contains(t, out, "not available")
	})

	t.Run("a malformed invocation keeps the default exit 1", func(t *testing.T) {
		code, out := runClustarr(t, append(base, "PATH="+emptyPath),
			"squasharr", "--role", "worker", "--namespace", "media", "--data-dir", tmp)
		require.Equal(t, 1, code, "output:\n%s", out)
		require.Contains(t, out, "--job is required")
	})

	t.Run("invalid source: a TranscodeJob that does not exist exits 3", func(t *testing.T) {
		if os.Getenv("KUBEBUILDER_ASSETS") == "" {
			t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
		}
		env := &envtest.Environment{
			CRDDirectoryPaths:     []string{"../../config/crd/bases"},
			ErrorIfCRDPathMissing: true,
		}
		_, err := env.Start()
		require.NoError(t, err)
		t.Cleanup(func() { _ = env.Stop() })
		realKubeconfig := filepath.Join(tmp, "envtest-kubeconfig")
		require.NoError(t, os.WriteFile(realKubeconfig, env.KubeConfig, 0o600))

		// The worker checks for ffmpeg and ffprobe before anything else, so
		// a missing binary is never misread as an invalid source. These
		// stand-ins satisfy that check; neither is ever run, because the
		// TranscodeJob Get fails first.
		fakeBin := filepath.Join(tmp, "fake-bin")
		require.NoError(t, os.Mkdir(fakeBin, 0o755))
		for _, name := range []string{"ffmpeg", "ffprobe"} {
			require.NoError(t, os.WriteFile(filepath.Join(fakeBin, name), []byte("#!/bin/sh\nexit 0\n"), 0o755))
		}

		code, out := runClustarr(t, []string{"HOME=" + tmp, "KUBECONFIG=" + realKubeconfig, "PATH=" + fakeBin},
			"squasharr", "--role", "worker", "--job", "no-such-job", "--namespace", "default", "--data-dir", tmp)
		require.Equal(t, worker.ExitInvalidSource, code, "output:\n%s", out)
		require.Contains(t, out, "not found")
	})
}

// exitCode is main's mapping; the process test above is the proof, this
// pins the edge a process test cannot reach cheaply.
func TestExitCodeOnlyHonoursTheSquasharrWorker(t *testing.T) {
	require.Equal(t, 1, exitCode(errors.New("anything")))
	require.Equal(t, 4, exitCode(&squasharr.ExitError{Code: 4, Err: errors.New("verify")}))
	// An exec.ExitError also has ExitCode(); an ffmpeg status wrapped in
	// some other service's error must not become clustarr's.
	cmd := exec.Command("/bin/sh", "-c", "exit 3")
	err := cmd.Run()
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee)
	require.Equal(t, 1, exitCode(errors.Join(errors.New("wrapped"), err)))
}

// rbacGrant is one (API group, resource, verb) permission.
type rbacGrant struct{ group, resource, verb string }

func grantsOf(rules []rbacv1.PolicyRule) map[rbacGrant]bool {
	out := map[rbacGrant]bool{}
	for _, r := range rules {
		for _, g := range r.APIGroups {
			for _, res := range r.Resources {
				for _, v := range r.Verbs {
					out[rbacGrant{g, res, v}] = true
				}
			}
		}
	}
	return out
}

func sortedGrants(m map[rbacGrant]bool) []string {
	var out []string
	for g := range m {
		out = append(out, g.group+"/"+g.resource+":"+g.verb)
	}
	sort.Strings(out)
	return out
}

func readWorkerRole(t *testing.T, root string) rbacv1.ClusterRole {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "config", "rbac", "squasharr_worker_role.yaml"))
	require.NoError(t, err)
	var role rbacv1.ClusterRole
	require.NoError(t, yaml.Unmarshal(raw, &role))
	require.NotEmpty(t, role.Rules, "config/rbac/squasharr_worker_role.yaml has no rules")
	return role
}

// TestSquasharrWorkerRoleMatchesTheWorkerMarkers holds the generated worker
// ClusterRole to the marker TEXT in squasharr/worker, grant for grant.
//
// The markers are the single source (squasharr/worker/doc.go): `make
// manifests` generates the Role from that package alone. This reads the
// markers without controller-gen, so a marker added and never regenerated
// -- the worker Getting something its pod is Forbidden to Get, which no
// envtest can see -- fails here, and so does a hand edit to the Role.
func TestSquasharrWorkerRoleMatchesTheWorkerMarkers(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	want := map[rbacGrant]bool{}
	dir := filepath.Join(root, "squasharr", "worker")
	require.NoError(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir {
				return fs.SkipDir // controller-gen reads this one package, not its children
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			_, marker, found := strings.Cut(line, "+kubebuilder:rbac:")
			if !found {
				continue
			}
			fields := map[string][]string{}
			for _, field := range strings.Split(strings.TrimSpace(marker), ",") {
				if k, v, ok := strings.Cut(field, "="); ok {
					fields[strings.TrimSpace(k)] = strings.Split(strings.Trim(strings.TrimSpace(v), `"`), ";")
				}
			}
			for _, g := range fields["groups"] {
				for _, r := range fields["resources"] {
					for _, v := range fields["verbs"] {
						want[rbacGrant{g, r, v}] = true
					}
				}
			}
		}
		return nil
	}))
	require.NotEmpty(t, want, "no +kubebuilder:rbac markers under squasharr/worker; this guard is looking in the wrong place")

	got := grantsOf(readWorkerRole(t, root).Rules)
	require.Equal(t, sortedGrants(want), sortedGrants(got),
		"config/rbac/squasharr_worker_role.yaml no longer matches squasharr/worker's +kubebuilder:rbac markers.\n"+
			"Run `make manifests` (it regenerates the worker Role from that package alone), then copy its rules "+
			"into charts/clustarr/templates/rbac.yaml between the squasharr_worker_role BEGIN/END sentinels.")
}

// TestChartWorkerRBACMatchesTheGeneratedRole is TestChartRBACMatchesTheGeneratedRole
// for the worker's ClusterRole: Helm cannot read config/, so the chart carries
// a verbatim copy, and this is what keeps it verbatim.
func TestChartWorkerRBACMatchesTheGeneratedRole(t *testing.T) {
	const (
		begin = "# BEGIN generated from config/rbac/squasharr_worker_role.yaml -- do not edit by hand.\n"
		end   = "# END generated from config/rbac/squasharr_worker_role.yaml."
	)
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	generated, err := os.ReadFile(filepath.Join(root, "config", "rbac", "squasharr_worker_role.yaml"))
	require.NoError(t, err)
	_, want, found := strings.Cut(string(generated), "\nrules:\n")
	require.True(t, found, "config/rbac/squasharr_worker_role.yaml has no rules: block")

	chart, err := os.ReadFile(filepath.Join(root, "charts", "clustarr", "templates", "rbac.yaml"))
	require.NoError(t, err)
	_, after, found := strings.Cut(string(chart), begin)
	require.True(t, found, "charts/clustarr/templates/rbac.yaml has no %q sentinel", strings.TrimSpace(begin))
	got, _, found := strings.Cut(after, end)
	require.True(t, found, "charts/clustarr/templates/rbac.yaml has no %q sentinel", end)

	require.Equal(t, strings.TrimRight(want, "\n"), strings.TrimRight(got, "\n"),
		"the chart's squasharr-worker ClusterRole has drifted from config/rbac/squasharr_worker_role.yaml.\n"+
			"Run `make manifests`, then copy the rules: list between the BEGIN/END sentinels.")
}

// rendered is one installer's output, decoded into the kinds this test reads.
type rendered struct {
	deployments     []appsv1.Deployment
	serviceAccounts map[string]bool
	claims          map[string]bool
	roles           map[string]rbacv1.ClusterRole
	bindings        []rbacv1.ClusterRoleBinding
}

func decodeRendered(t *testing.T, in []byte) rendered {
	t.Helper()
	r := rendered{serviceAccounts: map[string]bool{}, claims: map[string]bool{}, roles: map[string]rbacv1.ClusterRole{}}
	dec := utilyaml.NewYAMLOrJSONDecoder(strings.NewReader(string(in)), 4096)
	for {
		var obj map[string]any
		if err := dec.Decode(&obj); errors.Is(err, io.EOF) {
			return r
		} else if err != nil {
			t.Fatalf("decode rendered manifests: %v", err)
		}
		if obj == nil {
			continue
		}
		into := func(v any) {
			require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(obj, v))
		}
		switch obj["kind"] {
		case "Deployment":
			var d appsv1.Deployment
			into(&d)
			r.deployments = append(r.deployments, d)
		case "ServiceAccount":
			var sa corev1.ServiceAccount
			into(&sa)
			r.serviceAccounts[sa.Name] = true
		case "PersistentVolumeClaim":
			var pvc corev1.PersistentVolumeClaim
			into(&pvc)
			r.claims[pvc.Name] = true
		case "ClusterRole":
			var cr rbacv1.ClusterRole
			into(&cr)
			r.roles[cr.Name] = cr
		case "ClusterRoleBinding":
			var crb rbacv1.ClusterRoleBinding
			into(&crb)
			r.bindings = append(r.bindings, crb)
		}
	}
}

// TestTranscodeJobServiceAccountHoldsTheWorkerRole answers "how do the
// Job's ServiceAccount and the worker's RBAC stay in sync" for both
// installers, from what they actually render.
//
// For each rendering it takes the squasharr Deployment, runs its argv
// through the real command tree with its env, and reads the
// --worker-service-account and --data-claim the TranscodeJob controller
// would stamp onto every Job. That account must exist, must be bound to a
// ClusterRole whose rules are exactly the generated worker Role -- and to
// nothing else, least of all the manager role -- and the claim must be one
// the installer creates. The chart is rendered under two release names,
// because its names carry the fullname and a hard-coded default would pass
// under "clustarr" and fail under anything else.
func TestTranscodeJobServiceAccountHoldsTheWorkerRole(t *testing.T) {
	helm := findTool(t, "helm")
	kustomize := findTool(t, "kustomize")
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	want := sortedGrants(grantsOf(readWorkerRole(t, root).Rules))

	cases := map[string][]byte{
		"helm template clustarr":   run(t, root, helm, "template", "clustarr", "charts/clustarr"),
		"helm template media":      run(t, root, helm, "template", "media", "charts/clustarr"),
		"kustomize config/default": run(t, root, kustomize, "build", "config/default"),
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			r := decodeRendered(t, out)
			var dep *appsv1.Deployment
			for i := range r.deployments {
				if r.deployments[i].Spec.Template.Labels["app.kubernetes.io/component"] == "squasharr" {
					dep = &r.deployments[i]
				}
			}
			require.NotNil(t, dep, "no squasharr Deployment rendered")
			ctr := dep.Spec.Template.Spec.Containers[0]

			// Exactly the Deployment's literal env; the four squasharr
			// variables are cleared first so this process's own
			// environment cannot stand in for a missing one.
			for _, e := range []string{workerImageEnv, workerImageCUDAEnv, workerServiceAccountEnv, dataClaimEnv} {
				t.Setenv(e, "")
			}
			for _, e := range ctr.Env {
				if e.ValueFrom == nil {
					t.Setenv(e.Name, e.Value)
				}
			}
			t.Setenv(namespaceEnv, "clustarr-system")
			got := stub(t, &runSquasharr)
			_, err := execute(t, ctr.Args...)
			require.NoError(t, err)
			require.NoError(t, got.Validate())

			account := got.WorkerServiceAccount
			require.True(t, r.serviceAccounts[account],
				"transcode Jobs would run as ServiceAccount %q, which %s does not create", account, name)
			require.True(t, r.claims[got.DataClaimName],
				"transcode Jobs would mount PVC %q, which %s does not create", got.DataClaimName, name)

			var boundTo []string
			for _, b := range r.bindings {
				for _, s := range b.Subjects {
					if s.Kind == "ServiceAccount" && s.Name == account {
						boundTo = append(boundTo, b.RoleRef.Name)
					}
				}
			}
			require.Len(t, boundTo, 1,
				"ServiceAccount %q should be bound to exactly the worker ClusterRole, and is bound to %v", account, boundTo)
			role, ok := r.roles[boundTo[0]]
			require.True(t, ok, "ServiceAccount %q is bound to ClusterRole %q, which %s does not render", account, boundTo[0], name)
			require.Equal(t, want, sortedGrants(grantsOf(role.Rules)),
				"the ClusterRole transcode Jobs run under is not the generated worker Role")
		})
	}
}
