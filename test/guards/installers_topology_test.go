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

package guards

import (
	"encoding/json"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"

	"github.com/mediactl/clustarr/pkg/agentdomain"
	"github.com/mediactl/clustarr/pkg/binpath"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/test/installtest"
)

// workloadRow is one row of spec §10.2.1's table, as both installers render it.
type workloadRow struct {
	image       string // repository, without the tag
	command     string // pkg/binpath
	replicas    int32  // 0: spec.replicas omitted, the manager's HPA owns it
	strategy    appsv1.DeploymentStrategyType
	grace       int64
	leaderElect bool
	domain      string // the agent's --domain; the autoscale label's value on autoscaled rows
	autoscaled  bool
	gomemPct    int64
}

const (
	clustarrRepo = "ghcr.io/mediactl/clustarr"
	nativeRepo   = "ghcr.io/mediactl/clustarr/native"
)

// workloadTable is spec §10.2.1. The download engines and the transcode
// pool and graft Jobs are absent: the manager renders them at runtime. The
// manager's grace is 85, not §3.5.6's 70: it drains catalog-segments-plan
// (AckWait 60 s), and TestGracePeriodsCoverAckWait holds every Deployment
// to its longest StopBudget plus 25 s. markers' is 60: it requeues what it
// holds on SIGTERM (RequeueOnStop) rather than waiting out its 30-minute
// AckWait, as it did with 1825 until 2026-10-15.
var workloadTable = map[string]workloadRow{
	"manager":        {image: clustarrRepo, command: binpath.Manager, replicas: 1, strategy: appsv1.RollingUpdateDeploymentStrategyType, grace: 85, leaderElect: true, gomemPct: 80},
	"agent-catalog":  {image: nativeRepo, command: binpath.Agent, strategy: appsv1.RollingUpdateDeploymentStrategyType, grace: 145, domain: "catalog", autoscaled: true, gomemPct: 80},
	"agent-events":   {image: nativeRepo, command: binpath.Agent, strategy: appsv1.RollingUpdateDeploymentStrategyType, grace: 55, domain: "events", autoscaled: true, gomemPct: 80},
	"agent-metadata": {image: nativeRepo, command: binpath.Agent, replicas: 1, strategy: appsv1.RecreateDeploymentStrategyType, grace: 85, domain: "metadata", gomemPct: 80},
	"agent-import":   {image: nativeRepo, command: binpath.Agent, strategy: appsv1.RollingUpdateDeploymentStrategyType, grace: 85, domain: "import", autoscaled: true, gomemPct: 60},
	"agent-index":    {image: nativeRepo, command: binpath.Agent, replicas: 1, strategy: appsv1.RecreateDeploymentStrategyType, grace: 85, domain: "index", gomemPct: 80},
	"agent-caption":  {image: nativeRepo, command: binpath.Agent, strategy: appsv1.RollingUpdateDeploymentStrategyType, grace: 115, domain: "caption", autoscaled: true, gomemPct: 60},
	"markers":        {image: nativeRepo, command: binpath.Markers, strategy: appsv1.RollingUpdateDeploymentStrategyType, grace: 60, domain: "markers", autoscaled: true, gomemPct: 50},
	"ui":             {image: clustarrRepo, command: binpath.UI, replicas: 1, strategy: appsv1.RollingUpdateDeploymentStrategyType, grace: 30, gomemPct: 80},
}

// installers renders both default installs: the chart as release
// "clustarr" (fullname "clustarr") and kustomize's config/default.
func installers(t *testing.T) map[string]installtest.Rendered {
	t.Helper()
	return map[string]installtest.Rendered{
		"helm":      installtest.DecodeRendered(t, installtest.RenderChart(t)),
		"kustomize": installtest.DecodeRendered(t, installtest.BuildKustomize(t, "config/default")),
	}
}

// repoOf strips the tag from an image reference.
func repoOf(image string) string {
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		return image[:i]
	}
	return image
}

func argIn(args []string, want string) bool { return slices.Contains(args, want) }

func containerEnv(c corev1.Container, name string) (string, bool) {
	for _, e := range c.Env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

func readRepoFile(t *testing.T, root, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	require.NoError(t, err)
	return b
}

// managerConsumers are the static durables the manager drains itself: every
// agentdomain.Fixed() consumer -- the segment planner's, the leader-only
// intakes (candidate, scan, advisory), the history sink and the DLQ
// projector (ADR-0019 §7.7, §8.2, §8.4; A2.5, A4.4). ADR-0016's fold
// retired the transcode results durable with its status writer (loop spec
// D25); the loop's records wakers are KV watches, not durables.
func managerConsumers() []string { return slices.Sorted(maps.Keys(agentdomain.Fixed())) }

// deploymentOf names the Deployment that runs a domain: agent-<domain>,
// except markers, which is its own binary and Deployment.
func deploymentOf(d agentdomain.Domain) string {
	if d.Name == agentdomain.Markers {
		return "markers"
	}
	return "agent-" + d.Name
}

// consumerHomes maps each static durable to every Deployment that binds it.
func consumerHomes() map[string][]string {
	homes := map[string][]string{}
	for _, c := range managerConsumers() {
		homes[c] = append(homes[c], "manager")
	}
	for _, d := range agentdomain.Domains() {
		for _, c := range d.Consumers {
			homes[c] = append(homes[c], deploymentOf(d))
		}
	}
	return homes
}

// TestInstallersRenderTheTopology holds both installers to spec §10.2.1:
// exactly its Deployments, each with its image, binary, replicas (or none,
// where the manager's HPA owns them), strategy, grace period, lease and
// autoscale declaration. Each binary's manifest_test.go parses the argv.
func TestInstallersRenderTheTopology(t *testing.T) {
	for name, r := range installers(t) {
		t.Run(name, func(t *testing.T) {
			got := installtest.Workloads(r)
			assert.Equal(t, slices.Sorted(maps.Keys(workloadTable)), slices.Sorted(maps.Keys(got)), "the Deployments of spec §10.2.1")
			for component, row := range workloadTable {
				d, ok := got[component]
				if !ok {
					continue
				}
				spec := d.Spec.Template.Spec
				require.Len(t, spec.Containers, 1, component)
				c := spec.Containers[0]
				assert.Equal(t, component, c.Name, "%s's container", component)
				assert.Equal(t, row.image, repoOf(c.Image), "%s's image", component)
				assert.Equal(t, []string{row.command}, c.Command, "%s names its binary: no image has an ENTRYPOINT", component)
				if row.replicas == 0 {
					assert.Nil(t, d.Spec.Replicas, "%s omits spec.replicas: the manager's HPA owns it", component)
				} else if assert.NotNil(t, d.Spec.Replicas, "%s sets spec.replicas", component) {
					assert.Equal(t, row.replicas, *d.Spec.Replicas, "%s's replicas", component)
				}
				strategy := d.Spec.Strategy.Type
				if strategy == "" {
					strategy = appsv1.RollingUpdateDeploymentStrategyType
				}
				assert.Equal(t, row.strategy, strategy, "%s's strategy", component)
				require.NotNil(t, spec.TerminationGracePeriodSeconds, component)
				assert.Equal(t, row.grace, *spec.TerminationGracePeriodSeconds, "%s's terminationGracePeriodSeconds", component)
				assert.Equal(t, row.leaderElect, argIn(c.Args, "--leader-elect"), "%s: only the manager passes --leader-elect", component)
				if row.command == binpath.Agent {
					assert.Contains(t, c.Args, "--domain="+row.domain, component)
				}
				if row.autoscaled {
					assert.Equal(t, row.domain, d.Labels[agentdomain.LabelDomain], "%s carries the autoscale label", component)
					require.Len(t, spec.TopologySpreadConstraints, 1, "%s spreads across nodes", component)
					tsc := spec.TopologySpreadConstraints[0]
					assert.Equal(t, "kubernetes.io/hostname", tsc.TopologyKey, component)
					assert.Equal(t, int32(1), tsc.MaxSkew, component)
					dom, ok := agentdomain.Lookup(row.domain)
					require.True(t, ok, row.domain)
					if dom.PerNode {
						// Hard, over the nodes the pod may land on: with
						// maxReplicas the matching nodes, one pod per node;
						// a surge pod still fits beside one (skew 1), so a
						// rollout never stalls (OD33).
						assert.Equal(t, corev1.DoNotSchedule, tsc.WhenUnsatisfiable, "%s runs a pod per node", component)
						require.NotNil(t, tsc.NodeTaintsPolicy, "%s counts only the nodes its pods tolerate, as MatchingNodes does", component)
						assert.Equal(t, corev1.NodeInclusionPolicyHonor, *tsc.NodeTaintsPolicy, component)
						require.NotNil(t, tsc.NodeAffinityPolicy, component)
						assert.Equal(t, corev1.NodeInclusionPolicyHonor, *tsc.NodeAffinityPolicy, component)
					} else {
						assert.Equal(t, corev1.ScheduleAnyway, tsc.WhenUnsatisfiable, "%s spreads softly", component)
					}
				} else {
					assert.NotContains(t, d.Labels, agentdomain.LabelDomain, component)
				}
			}
			if m, ok := got["manager"]; ok {
				ru := m.Spec.Strategy.RollingUpdate
				require.NotNil(t, ru, "the manager pins its rollout")
				require.NotNil(t, ru.MaxSurge)
				require.NotNil(t, ru.MaxUnavailable)
				assert.Equal(t, intstr.FromInt32(1), *ru.MaxSurge, "a new manager pod starts before the old one stops")
				assert.Equal(t, intstr.FromInt32(0), *ru.MaxUnavailable)
			}
		})
	}
}

// TestEveryAgentDeploymentRunsOneDomain (§10.3.2): every agent container
// passes exactly one --domain, each domain with consumers runs in exactly
// one Deployment named for it, and the artwork renderer is the catalog
// domain's and the artwork fetch the metadata domain's (R4). There is no
// `all`: one domain per process (§3.5.1).
func TestEveryAgentDeploymentRunsOneDomain(t *testing.T) {
	var want []string
	for _, d := range agentdomain.Domains() {
		if len(d.Consumers) > 0 && d.Name != agentdomain.Markers {
			want = append(want, d.Name)
		}
	}
	require.GreaterOrEqual(t, len(want), 6, "agentdomain lists %v; the guard expects the six agent domains", want)
	for name, r := range installers(t) {
		t.Run(name, func(t *testing.T) {
			seen := map[string]int{}
			for component, d := range installtest.Workloads(r) {
				c := d.Spec.Template.Spec.Containers[0]
				if !slices.Equal(c.Command, []string{binpath.Agent}) {
					continue
				}
				var domains []string
				for _, a := range c.Args {
					if v, ok := strings.CutPrefix(a, "--domain="); ok {
						domains = append(domains, v)
					}
					assert.NotEqual(t, "--domain", a, "%s: --domain takes its value inline", component)
				}
				require.Len(t, domains, 1, "%s passes exactly one --domain (one domain per process, §3.5.1)", component)
				assert.NotEqual(t, "all", domains[0], "%s: there is no all", component)
				assert.Equal(t, "agent-"+domains[0], component)
				seen[domains[0]]++
			}
			for _, d := range want {
				assert.Equal(t, 1, seen[d], "--domain=%s runs in exactly one Deployment", d)
			}
			assert.Len(t, seen, len(want), "a Deployment runs a domain agentdomain does not list: %v", seen)
		})
	}
	catalog, ok := agentdomain.Lookup(agentdomain.Catalog)
	require.True(t, ok)
	assert.Contains(t, catalog.Consumers, events.ConsumerCatalogArtworkRender, "the renderer runs in the catalog domain")
	metadata, ok := agentdomain.Lookup(agentdomain.Metadata)
	require.True(t, ok)
	assert.Contains(t, metadata.Consumers, events.ConsumerCatalogArtworkFetch, "the fetch runs in the metadata domain")
}

// TestDataMountsMatchTheTopology: /data is mounted read-write, with UMASK
// 002, exactly by the manager (its filesystem reconcilers, §5.14),
// agent-import and agent-caption; read-only by markers; by nothing else.
func TestDataMountsMatchTheTopology(t *testing.T) {
	want := map[string]string{"manager": "rw", "agent-import": "rw", "agent-caption": "rw", "markers": "ro"}
	for name, r := range installers(t) {
		t.Run(name, func(t *testing.T) {
			got := map[string]string{}
			for component, d := range installtest.Workloads(r) {
				spec := d.Spec.Template.Spec
				mode := ""
				for _, v := range spec.Volumes {
					if v.Name != "data" || v.PersistentVolumeClaim == nil {
						continue
					}
					assert.Equal(t, "clustarr-data", v.PersistentVolumeClaim.ClaimName, component)
					assert.True(t, r.Claims[v.PersistentVolumeClaim.ClaimName], "%s mounts a claim %s does not create", component, name)
					mode = "rw"
					for _, m := range spec.Containers[0].VolumeMounts {
						if m.Name == "data" {
							assert.Equal(t, "/data", m.MountPath, component)
							if m.ReadOnly {
								mode = "ro"
							}
						}
					}
				}
				got[component] = mode
				assert.Equal(t, want[component], mode, "%s's /data mount", component)
				umask, set := containerEnv(spec.Containers[0], "UMASK")
				assert.Equal(t, want[component] == "rw", set, "%s: UMASK exactly where /data is written", component)
				if set {
					assert.Equal(t, "002", umask, component)
				}
			}
			for component := range want {
				assert.Contains(t, got, component, "%s renders no %s Deployment", name, component)
			}
		})
	}
}

// TestGracePeriodsCoverAckWait: every Deployment's grace covers the longest
// StopBudget among the durables it drains plus 25 s (§3.5.6), so a SIGTERMed
// pod finishes or releases what it holds before it is killed. StopBudget is
// AckWait, the handler budget, not the broker's BackOff[0] deadline -- or,
// for a RequeueOnStop durable (markers-analyze), events.RequeueDrain: its
// handlers are cancelled at once and their deliveries requeued.
func TestGracePeriodsCoverAckWait(t *testing.T) {
	top := events.Default()
	drains := map[string][]string{"manager": managerConsumers()}
	for _, d := range agentdomain.Domains() {
		if len(d.Consumers) > 0 {
			drains[deploymentOf(d)] = d.Consumers
		}
	}
	for name, r := range installers(t) {
		t.Run(name, func(t *testing.T) {
			var checked int
			for component, d := range installtest.Workloads(r) {
				var longest time.Duration
				for _, c := range drains[component] {
					spec, ok := top.Consumer(c)
					require.True(t, ok, "%s drains %s, which events.Default() does not declare", component, c)
					longest = max(longest, spec.StopBudget())
				}
				if longest == 0 {
					continue
				}
				checked++
				grace := d.Spec.Template.Spec.TerminationGracePeriodSeconds
				require.NotNil(t, grace, component)
				assert.GreaterOrEqual(t, time.Duration(*grace)*time.Second, longest+25*time.Second,
					"%s's terminationGracePeriodSeconds must cover its longest StopBudget (%s) plus 25 s", component, longest)
			}
			require.GreaterOrEqual(t, checked, 7, "%s: only %d Deployments drain a durable; the guard is not reading the topology", name, checked)
		})
	}
}

// TestAutoscaledDeploymentsAreDeclared (§10.2.2): a Deployment carries the
// autoscale label iff agentdomain marks its domain autoscaled, such a
// Deployment omits spec.replicas (an explicit 0 would never be woken) and
// every other sets it, CLUSTARR_CONSUMER_SLOTS parses and names only that
// domain's consumers, and autoscaling.enabled=false falls back to fixed
// replicas and tells the manager.
func TestAutoscaledDeploymentsAreDeclared(t *testing.T) {
	autoscaled := map[string]agentdomain.Domain{}
	for _, d := range agentdomain.Domains() {
		if d.Autoscaled {
			autoscaled[deploymentOf(d)] = d
		}
	}
	require.NotEmpty(t, autoscaled)
	for name, r := range installers(t) {
		t.Run(name, func(t *testing.T) {
			for component, d := range installtest.Workloads(r) {
				dom, want := autoscaled[component]
				label, has := d.Labels[agentdomain.LabelDomain]
				assert.Equal(t, want, has, "%s: the autoscale label iff its domain is autoscaled", component)
				if want {
					assert.Equal(t, dom.Name, label, component)
					assert.Nil(t, d.Spec.Replicas, "%s omits spec.replicas", component)
				} else {
					assert.NotNil(t, d.Spec.Replicas, "%s sets spec.replicas", component)
				}
				assertConsumerSlots(t, component, d, dom)
			}
		})
	}
	var sets []string
	for _, d := range agentdomain.Domains() {
		if !d.Autoscaled || len(d.Consumers) == 0 {
			continue
		}
		key := "agents." + d.Name
		if d.Name == agentdomain.Markers {
			key = "markers"
		}
		sets = append(sets, "--set", key+".consumerSlots."+d.Consumers[0]+"=3")
	}
	var slotted int
	for component, d := range installtest.Workloads(installtest.DecodeRendered(t, installtest.RenderChart(t, sets...))) {
		dom, ok := autoscaled[component]
		if !ok {
			continue
		}
		slotted++
		v, set := containerEnv(d.Spec.Template.Spec.Containers[0], events.SlotsEnv)
		require.True(t, set, "%s renders no %s from consumerSlots", component, events.SlotsEnv)
		assert.Equal(t, dom.Consumers[0]+"=3", v, component)
		assertConsumerSlots(t, component, d, dom)
	}
	assert.Equal(t, len(autoscaled), slotted, "every autoscaled domain renders its consumerSlots")
	off := installtest.Workloads(installtest.DecodeRendered(t, installtest.RenderChart(t, "--set", "autoscaling.enabled=false")))
	require.NotEmpty(t, off)
	for component, d := range off {
		assert.NotContains(t, d.Labels, agentdomain.LabelDomain, "%s with autoscaling off", component)
		assert.NotNil(t, d.Spec.Replicas, "%s sets replicas with autoscaling off", component)
	}
	assert.Contains(t, off["manager"].Spec.Template.Spec.Containers[0].Args, "--autoscale=false")
}

func assertConsumerSlots(t *testing.T, component string, d appsv1.Deployment, dom agentdomain.Domain) {
	t.Helper()
	v, ok := containerEnv(d.Spec.Template.Spec.Containers[0], events.SlotsEnv)
	if !ok {
		return
	}
	slots, err := events.ParseSlotOverrides(v)
	require.NoError(t, err, "%s: %s=%q", component, events.SlotsEnv, v)
	for c := range slots {
		assert.Contains(t, dom.Consumers, c, "%s: %s names %s, not a consumer of domain %q", component, events.SlotsEnv, c, dom.Name)
	}
}

// TestEveryConsumerHasExactlyOneHome: the autoscaler and the processes must
// count the consumers that really exist. Every static durable of
// events.Default() is bound by exactly one Deployment -- the manager, one
// agent domain or markers -- so a durable nothing drains (its queue only
// grows) or two processes drain (the manager's HPA would count one home's
// lag for both) fails here, and the manager-homed ones are exactly
// agentdomain.Fixed()'s. The transcode pools' durables are dynamic
// (events.TranscodeTaskConsumerName) and not in Default(). Every durable
// has exactly one dead-letter watcher on StreamAdvisories (§5.9).
func TestEveryConsumerHasExactlyOneHome(t *testing.T) {
	top := events.Default()
	homes := consumerHomes()
	fixed := agentdomain.Fixed()
	durables := map[string]events.ConsumerSpec{}
	for _, c := range top.Consumers {
		if c.Stream == events.StreamAdvisories {
			continue
		}
		durables[c.Name] = c
		switch h := homes[c.Name]; len(h) {
		case 0:
			t.Errorf("durable %s on %s has no home: no Deployment binds it, so its stream only grows", c.Name, c.Stream)
		case 1:
		default:
			t.Errorf("durable %s is bound by %v: exactly one Deployment may drain a durable", c.Name, h)
		}
		_, isFixed := fixed[c.Name]
		inAgent := len(homes[c.Name]) == 1 && homes[c.Name][0] != "manager"
		assert.NotEqual(t, isFixed, inAgent,
			"durable %s must be in exactly one of agentdomain.Domains() and agentdomain.Fixed()", c.Name)
	}
	require.GreaterOrEqual(t, len(durables), 15, "events.Default() declares only %d durables; this guard is not reading the topology", len(durables))
	for _, r := range top.Retired {
		assert.Empty(t, homes[r.Durable], "retired durable %s on %s still has a home", r.Durable, r.Stream)
		_, declared := durables[r.Durable]
		assert.False(t, declared, "retired durable %s is still a topology consumer", r.Durable)
	}
	for name, h := range homes {
		_, ok := durables[name]
		assert.True(t, ok, "%v binds %s, which events.Default() does not declare", h, name)
	}
	for _, c := range top.Consumers {
		if c.Stream != events.StreamAdvisories {
			continue
		}
		var watched []string
		for _, d := range durables {
			if events.DeadLetterWatcherName(d.Stream, d.Name) == c.Name {
				watched = append(watched, d.Name)
			}
		}
		assert.Len(t, watched, 1, "dead-letter watcher %s must watch exactly one durable of the topology", c.Name)
	}
	for _, d := range durables {
		w := events.DeadLetterWatcherName(d.Stream, d.Name)
		_, ok := top.Consumer(w)
		assert.True(t, ok, "durable %s has no dead-letter watcher %s in the topology (§5.9)", d.Name, w)
	}
}

// TestNoInstallerRendersAnHPAOrKEDA: the manager's autoscale reconciler owns
// every HorizontalPodAutoscaler of the queue-driven workloads (R5) -- Helm's
// server-side apply would fight it over maxReplicas -- and KEDA is gone
// (§10.2.7): no keda.sh object in any render, no keda value, schema key or
// chart dependency, no config/keda. The one HPA the chart renders is the
// ui's, opt in under ui.autoscaling and never the manager's
// (ui_autoscaling_test.go); off by default, so none appears here.
func TestNoInstallerRendersAnHPAOrKEDA(t *testing.T) {
	root := installtest.RepoRoot(t)
	for name, out := range map[string][]byte{
		"helm template":                  installtest.RenderChart(t),
		"helm template, servicemonitors": installtest.RenderChart(t, "--set", "metrics.serviceMonitor.enabled=true"),
		"kustomize config/default":       installtest.BuildKustomize(t, "config/default"),
		"kustomize config/e2e":           installtest.BuildKustomize(t, "config/e2e"),
	} {
		objs := installtest.Objects(t, out)
		require.NotEmpty(t, objs, name)
		for _, o := range objs {
			gvk := o.GroupVersionKind()
			assert.NotEqual(t, "keda.sh", gvk.Group, "%s renders %s %q: KEDA is removed", name, gvk.Kind, o.GetName())
			assert.NotEqual(t, "HorizontalPodAutoscaler", gvk.Kind,
				"%s renders HorizontalPodAutoscaler %q: the manager's autoscale reconciler owns every HPA", name, o.GetName())
		}
	}
	var values map[string]any
	require.NoError(t, yaml.Unmarshal(readRepoFile(t, root, "charts/clustarr/values.yaml"), &values))
	assert.NotContains(t, values, "keda", "values.yaml")
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(readRepoFile(t, root, "charts/clustarr/values.schema.json"), &schema))
	require.NotEmpty(t, schema.Properties, "values.schema.json")
	if k, ok := schema.Properties["keda"]; ok {
		// W6.13 names a retired key only to reject it: a const that no value
		// can match, which says where it went.
		retired, _ := k.(map[string]any)["const"].(string)
		assert.True(t, strings.HasPrefix(retired, "retired"), "values.schema.json accepts a keda block: %v", k)
	}
	for _, f := range []string{"charts/clustarr/Chart.yaml", "charts/clustarr/Chart.lock"} {
		var c struct {
			Dependencies []struct{ Name string } `json:"dependencies"`
		}
		require.NoError(t, yaml.Unmarshal(readRepoFile(t, root, f), &c), f)
		require.NotEmpty(t, c.Dependencies, f)
		for _, d := range c.Dependencies {
			assert.NotEqual(t, "keda", d.Name, "%s depends on keda", f)
		}
	}
	_, err := os.Stat(filepath.Join(root, "config", "keda"))
	assert.True(t, os.IsNotExist(err), "config/keda is gone")
}

// TestEveryClustarrDeploymentCarriesNATSURL (W5.16): every clustarr
// Deployment's NATS_URL dials a Service the same installer creates, on its
// client port -- a URL naming a Service no installer renders is a binary
// retrying forever against nothing, which nats.go's retry hides.
func TestEveryClustarrDeploymentCarriesNATSURL(t *testing.T) {
	for name, r := range installers(t) {
		t.Run(name, func(t *testing.T) {
			services := map[string][]int32{}
			for _, s := range r.Services {
				for _, p := range s.Spec.Ports {
					services[s.Name] = append(services[s.Name], p.Port)
				}
			}
			workloads := installtest.Workloads(r)
			require.Len(t, workloads, len(workloadTable))
			for component, d := range workloads {
				raw, ok := containerEnv(d.Spec.Template.Spec.Containers[0], "NATS_URL")
				require.True(t, ok && raw != "", "%s sets no NATS_URL", component)
				for _, entry := range strings.Split(raw, ",") {
					u, err := url.Parse(entry)
					require.NoError(t, err, "%s: NATS_URL %q", component, entry)
					assert.Equal(t, "nats", u.Scheme, component)
					host := strings.Split(u.Hostname(), ".")[0]
					ports, ok := services[host]
					require.True(t, ok, "%s dials %s, a Service %s does not render", component, u.Hostname(), name)
					assert.Contains(t, ports, int32(mustAtoi(t, u.Port())), "%s dials port %s, which Service %s does not expose", component, u.Port(), host)
				}
			}
		})
	}
}

// TestEveryClustarrContainerCarriesNATSURL: every binary dials NATS, so
// every container whose command is a pkg/binpath binary carries NATS_URL --
// without it the binary dials a default Service no installer creates, and
// nats.go's retry hides the failure -- and one installer names one endpoint.
func TestEveryClustarrContainerCarriesNATSURL(t *testing.T) {
	binaries := map[string]bool{binpath.Manager: true, binpath.UI: true, binpath.Agent: true, binpath.Markers: true, binpath.Transcode: true}
	for name, r := range installers(t) {
		t.Run(name, func(t *testing.T) {
			urls := map[string]string{}
			for component, d := range installtest.Workloads(r) {
				c := d.Spec.Template.Spec.Containers[0]
				if len(c.Command) != 1 || !binaries[c.Command[0]] {
					continue
				}
				v, ok := containerEnv(c, "NATS_URL")
				require.True(t, ok && v != "", "%s runs %s and sets no NATS_URL", component, c.Command[0])
				urls[component] = v
			}
			require.Len(t, urls, len(workloadTable), "every Deployment of §10.2.1 runs a pkg/binpath binary")
			for component, v := range urls {
				assert.Equal(t, urls["manager"], v, "%s dials a different NATS from the manager", component)
			}
		})
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	require.NotEmpty(t, s, "a NATS URL names its port")
	for _, r := range s {
		require.True(t, r >= '0' && r <= '9', "port %q", s)
		n = n*10 + int(r-'0')
	}
	return n
}
