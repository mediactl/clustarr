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

package manager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	transcodemanager "github.com/mediactl/clustarr/app/transcode/manager"
	"github.com/mediactl/clustarr/pkg/placement"
)

var baseArgs = []string{
	"--nats-url=nats://nats:4222", "--native-image=clustarr/native:dev", "--leader-election-namespace=clustarr-system",
}

// TestWorkerPlacementFlagReachesPoolAndEngines: --worker-placement is the
// workers profile the chart renders (placement design §5.6), and it reaches
// both registrations that build pods: transcode's pool configuration and
// grab's engine options.
func TestWorkerPlacementFlagReachesPoolAndEngines(t *testing.T) {
	var got Options
	cmd := NewCommandWith(func(_ context.Context, o Options) error { got = o; return nil })
	cmd.SetArgs(append(baseArgs, `--worker-placement={"nodeAffinity":[{"key":"k","operator":"Exists"}]}`))
	require.NoError(t, cmd.Execute())

	want := placement.Profile{NodeAffinity: []corev1.NodeSelectorRequirement{{Key: "k", Operator: corev1.NodeSelectorOpExists}}}
	assert.Equal(t, want, got.WorkerPlacement)
	assert.Equal(t, want, transcodemanager.PoolConfig(transcodeOptions(got, nil)).WorkerPlacement)
	assert.Equal(t, want, grabOptions(got).WorkerPlacement)
}

func TestNoWorkerPlacementFlagIsTheZeroProfile(t *testing.T) {
	var got Options
	cmd := NewCommandWith(func(_ context.Context, o Options) error { got = o; return nil })
	cmd.SetArgs(baseArgs)
	require.NoError(t, cmd.Execute())
	assert.True(t, got.WorkerPlacement.IsZero())
}

// TestInvalidWorkerPlacementStopsTheManager: a manager never starts with no
// placement because its flag did not parse (the plan's Review Focus 4).
func TestInvalidWorkerPlacementStopsTheManager(t *testing.T) {
	ran := false
	cmd := NewCommandWith(func(context.Context, Options) error { ran = true; return nil })
	cmd.SetArgs(append(baseArgs, "--worker-placement={"))
	cmd.SilenceUsage, cmd.SilenceErrors = true, true

	err := cmd.Execute()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "--worker-placement")
	assert.False(t, ran)
}
