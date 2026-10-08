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
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/controller/downloadclient"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/pkg/binpath"
	"github.com/mediactl/clustarr/test/installtest"
)

// shippedBinaries is what each image's Dockerfile copies into /usr/bin, by
// repository.
func shippedBinaries(t *testing.T, root string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for repo, file := range map[string]string{clustarrRepo: "images/Dockerfile.clustarr", nativeRepo: "images/Dockerfile.native"} {
		m := regexp.MustCompile(`(?m)^COPY --from=build ((?:/out/\w+ )+)/usr/bin/$`).FindStringSubmatch(string(readRepoFile(t, root, file)))
		require.NotNil(t, m, "%s copies no binary into /usr/bin", file)
		for _, f := range strings.Fields(m[1]) {
			out[repo] = append(out[repo], "/usr/bin/"+strings.TrimPrefix(f, "/out/"))
		}
	}
	return out
}

// TestEveryContainerNamesItsBinary (§10.3.2): no image has an ENTRYPOINT, so
// every installer container, every engine container the manager renders
// (downloadclient.EngineCommand) and every pool container (pool.Template)
// names a pkg/binpath binary its image ships.
func TestEveryContainerNamesItsBinary(t *testing.T) {
	root := installtest.RepoRoot(t)
	shipped := shippedBinaries(t, root)
	assert.ElementsMatch(t, []string{binpath.Manager, binpath.UI}, shipped[clustarrRepo])
	assert.ElementsMatch(t, []string{binpath.Agent, binpath.Markers, binpath.Transcode}, shipped[nativeRepo])
	for name, r := range installers(t) {
		workloads := installtest.Workloads(r)
		require.Len(t, workloads, len(workloadTable), name)
		for component, d := range workloads {
			c := d.Spec.Template.Spec.Containers[0]
			require.Len(t, c.Command, 1, "%s %s names exactly one binary", name, component)
			assert.Contains(t, shipped[repoOf(c.Image)], c.Command[0], "%s %s runs %s, which %s does not ship", name, component, c.Command[0], c.Image)
		}
	}
	for _, dc := range []*downloadv1alpha1.DownloadClient{
		{ObjectMeta: metav1.ObjectMeta{Name: "qbt"}, Spec: downloadv1alpha1.DownloadClientSpec{Protocol: commonv1alpha1.ProtocolTorrent, Torrent: &downloadv1alpha1.TorrentSpec{}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "sab"}, Spec: downloadv1alpha1.DownloadClientSpec{Protocol: commonv1alpha1.ProtocolUsenet, Usenet: &downloadv1alpha1.UsenetSpec{}}},
	} {
		command, _ := downloadclient.EngineCommand(dc, "/data", "/scratch")
		require.Len(t, command, 1, dc.Name)
		assert.Contains(t, shipped[nativeRepo], command[0], "the %s engine runs on the native image", dc.Name)
	}
	for _, class := range []transcodev1alpha1.Hardware{"cpu", "nvidia", "intel"} {
		c := pool.Template(&transcodev1alpha1.TranscodeProfile{}, class, pool.Config{}).Spec.Containers[0]
		require.Len(t, c.Command, 1, string(class))
		assert.Contains(t, shipped[nativeRepo], c.Command[0], "the %s pool runs on the native image", class)
	}
}

// TestGOMEMLIMITIsItsShareOfTheLimit (§10.2.1, OD42): GOMEMLIMIT is
// round(limit x share) -- 60% for agent-import and agent-caption, 50% for
// markers, 80% elsewhere -- in both installers, and their defaults agree.
func TestGOMEMLIMITIsItsShareOfTheLimit(t *testing.T) {
	values := map[string]map[string]string{}
	for name, r := range installers(t) {
		values[name] = map[string]string{}
		for component, d := range installtest.Workloads(r) {
			row, ok := workloadTable[component]
			require.True(t, ok, component)
			c := d.Spec.Template.Spec.Containers[0]
			limit, ok := c.Resources.Limits[corev1.ResourceMemory]
			require.True(t, ok, "%s %s sets no memory limit", name, component)
			got, ok := containerEnv(c, "GOMEMLIMIT")
			require.True(t, ok, "%s %s sets no GOMEMLIMIT", name, component)
			assert.Equal(t, strconv.FormatInt((limit.Value()*row.gomemPct+50)/100, 10), got,
				"%s %s: GOMEMLIMIT is %d%% of %s", name, component, row.gomemPct, limit.String())
			values[name][component] = got
		}
		require.Len(t, values[name], len(workloadTable), name)
	}
	assert.Equal(t, values["helm"], values["kustomize"], "the chart's default limits and config/manager's")
}

// TestCredentialLessWorkloadsHoldNoToken: the transcode pool and graft
// Jobs and markers report over NATS and hold no Kubernetes credentials, so
// no installer renders an identity for the transcode pods (markers' own
// account is TestMarkersHasNoCredentials').
func TestCredentialLessWorkloadsHoldNoToken(t *testing.T) {
	helm := installtest.FindTool(t, "helm")
	root := installtest.RepoRoot(t)
	for name, out := range map[string][]byte{
		"helm template clustarr":   installtest.RenderChart(t),
		"helm template media":      installtest.Run(t, root, helm, "template", "media", "charts/clustarr"),
		"kustomize config/default": installtest.BuildKustomize(t, "config/default"),
	} {
		r := installtest.DecodeRendered(t, out)
		var names []string
		for sa := range r.ServiceAccounts {
			names = append(names, "ServiceAccount "+sa)
		}
		for role := range r.Roles {
			names = append(names, "ClusterRole "+role)
		}
		for _, b := range r.Bindings {
			names = append(names, "ClusterRoleBinding "+b.Name)
		}
		for _, b := range r.RoleBindings {
			names = append(names, "RoleBinding "+b.Name)
		}
		require.GreaterOrEqual(t, len(names), 20, "%s renders only %d identities and bindings", name, len(names))
		for _, n := range names {
			for _, gone := range []string{"worker", "transcode", "pool", "graft"} {
				assert.NotContains(t, n, gone, "%s renders %s: the transcode pods hold no identity", name, n)
			}
		}
	}
	// The pool pods the manager renders mount no token and name no account.
	for _, class := range []transcodev1alpha1.Hardware{"cpu", "nvidia", "intel"} {
		spec := pool.Template(&transcodev1alpha1.TranscodeProfile{}, class, pool.Config{}).Spec
		require.NotNil(t, spec.AutomountServiceAccountToken, "the %s pool pod states its token mount", class)
		assert.False(t, *spec.AutomountServiceAccountToken, "the %s pool pod mounts no token", class)
		assert.Empty(t, spec.ServiceAccountName, "the %s pool pod runs as no named account", class)
	}
}
