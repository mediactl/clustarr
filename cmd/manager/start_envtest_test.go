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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/internal/cli/manager"
)

// TestServiceFailsFastOnBadOptions proves the validation runs before anything
// touches the cluster: no kubeconfig and no NATS server are needed for these.
// The manager has no --role (one process runs every controller, spec §3.4),
// so the old unknown-role case has no subject.
func TestServiceFailsFastOnBadOptions(t *testing.T) {
	ctx := context.Background()

	o := manager.DefaultOptions()
	o.NativeImage = "ghcr.io/mediactl/clustarr/native:test"
	o.NATSURL = ""
	require.ErrorContains(t, manager.Run(ctx, o), "--nats-url",
		"a missing --nats-url reached the manager")

	o = manager.DefaultOptions()
	o.NativeImage = "ghcr.io/mediactl/clustarr/native:test"
	o.NATSURL = "nats://127.0.0.1:1"
	o.LeaderElect = true
	o.Namespace, o.LeaderElectionNamespace = "", ""
	require.ErrorContains(t, manager.Run(ctx, o), "leader election needs a namespace",
		"leader election with nowhere to put the Lease reached the manager")
}
