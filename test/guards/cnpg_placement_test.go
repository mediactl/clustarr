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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/mediactl/clustarr/test/installtest"
)

// renderedCluster is the rendered CloudNativePG Cluster named name.
func renderedCluster(t *testing.T, out []byte, name string) map[string]any {
	t.Helper()
	for _, o := range installtest.Objects(t, out) {
		if o.GetKind() == "Cluster" && o.GetName() == name {
			return o.Object
		}
	}
	t.Fatalf("no Cluster %s rendered", name)
	return nil
}

func nested(t *testing.T, obj map[string]any, path ...string) any {
	t.Helper()
	v, found, err := unstructured.NestedFieldNoCopy(obj, path...)
	require.NoError(t, err)
	if !found {
		return nil
	}
	return v
}

func exprs(t *testing.T, obj map[string]any) []any {
	t.Helper()
	terms, _ := nested(t, obj, "spec", "affinity", "nodeAffinity", "requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms").([]any)
	require.Len(t, terms, 1)
	es, _ := terms[0].(map[string]any)["matchExpressions"].([]any)
	return es
}

// TestPlexDatabaseReplicationSettings is the control-plane placement design's
// §5.1: three instances, one per control-plane node, quorum-synchronous to
// either standby with preferred durability and no failover quorum, a capped
// slot WAL, and switchover updates.
func TestPlexDatabaseReplicationSettings(t *testing.T) {
	out := installtest.RenderChart(t, placementOn("--set", "cluster-plex.enabled=true", "--set-json", "cluster-plex.plex.affinity="+cpAffinity)...)
	c := renderedCluster(t, out, "clustarr-plex-db")

	assert.EqualValues(t, 3, nested(t, c, "spec", "instances"))
	assert.Equal(t, true, nested(t, c, "spec", "affinity", "enablePodAntiAffinity"))
	assert.Equal(t, "required", nested(t, c, "spec", "affinity", "podAntiAffinityType"))
	assert.Equal(t, "kubernetes.io/hostname", nested(t, c, "spec", "affinity", "topologyKey"))
	assert.Equal(t, []any{map[string]any{"key": "node-role.kubernetes.io/control-plane", "operator": "Exists"}}, exprs(t, c))
	assert.Equal(t, []any{map[string]any{"key": "node-role.kubernetes.io/control-plane", "operator": "Exists", "effect": "NoSchedule"}},
		nested(t, c, "spec", "affinity", "tolerations"))
	sync, _ := nested(t, c, "spec", "postgresql", "synchronous").(map[string]any)
	require.NotNil(t, sync)
	assert.Equal(t, "any", sync["method"])
	assert.EqualValues(t, 1, sync["number"])
	assert.Equal(t, "preferred", sync["dataDurability"])
	assert.NotContains(t, sync, "failoverQuorum", "it would block a failover with the primary and a standby down")
	assert.Len(t, sync, 3)
	assert.Equal(t, "2GB", nested(t, c, "spec", "postgresql", "parameters", "max_slot_wal_keep_size"))
	assert.Equal(t, "off", nested(t, c, "spec", "postgresql", "parameters", "jit"), "the existing parameters stay")
	assert.Equal(t, "unsupervised", nested(t, c, "spec", "primaryUpdateStrategy"))
	assert.Equal(t, "switchover", nested(t, c, "spec", "primaryUpdateMethod"))
}

func TestPlexDatabaseWithPlacementOffIsUnchanged(t *testing.T) {
	out := installtest.RenderChart(t, "--set", "cluster-plex.enabled=true")
	c := renderedCluster(t, out, "clustarr-plex-db")

	assert.EqualValues(t, 1, nested(t, c, "spec", "instances"))
	assert.Nil(t, nested(t, c, "spec", "affinity"))
	assert.Nil(t, nested(t, c, "spec", "postgresql", "synchronous"))
	assert.Nil(t, nested(t, c, "spec", "postgresql", "parameters", "max_slot_wal_keep_size"))
	assert.Nil(t, nested(t, c, "spec", "primaryUpdateMethod"))
}

// TestReleaseIndexPostgresOnWorkers: everything but the four control-plane
// components runs on the workers (§5.5), the release index included.
func TestReleaseIndexPostgresOnWorkers(t *testing.T) {
	out := installtest.RenderChart(t, placementOn("--set", "postgres.enabled=true")...)
	c := renderedCluster(t, out, "clustarr-postgres")

	assert.Equal(t, []any{
		map[string]any{"key": "node-role.kubernetes.io/control-plane", "operator": "DoesNotExist"},
		map[string]any{"key": "kubernetes.io/arch", "operator": "In", "values": []any{"amd64"}},
	}, exprs(t, c))
	assert.Nil(t, nested(t, c, "spec", "affinity", "podAntiAffinityType"), "CNPG's preferred default")
	assert.EqualValues(t, 1, nested(t, c, "spec", "instances"))
}
