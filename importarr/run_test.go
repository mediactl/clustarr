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

package importarr_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/importarr"
)

func TestRunRejectsAnUnknownRole(t *testing.T) {
	err := importarr.Run(context.Background(), importarr.Options{Role: "nonsense"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "role")
}

func TestManagerOptionsUseTheImportarrLeaderElectionID(t *testing.T) {
	o := importarr.Options{Role: "controller", LeaderElect: true}
	mo := o.ManagerOptions()
	require.Equal(t, "importarr.clustarr.io", mo.LeaderElectionID)
	require.True(t, mo.LeaderElection)
}

func TestWorkerRoleDoesNotLeaderElect(t *testing.T) {
	o := importarr.Options{Role: "worker", LeaderElect: true}
	require.False(t, o.ManagerOptions().LeaderElection,
		"workers are queue consumers; electing a leader would idle every other replica")
}

func TestAllRoleLeaderElectsWhenAsked(t *testing.T) {
	o := importarr.Options{Role: "all", LeaderElect: true}
	require.True(t, o.ManagerOptions().LeaderElection,
		"the all role runs controllers too, so it takes the lease like a dedicated controller replica")
}

func TestValidateRejectsAnEmptyDataPath(t *testing.T) {
	o := importarr.Options{Role: "worker", DataPath: "  "}
	err := o.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "data-path")
}

func TestValidateRequiresTheBus(t *testing.T) {
	o := importarr.DefaultOptions()
	o.NATSURL = ""
	err := o.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "nats-url")
}

func TestDataReadyCheckerRejectsAMissingDirectory(t *testing.T) {
	err := importarr.DataReadyChecker(t.TempDir() + "/does-not-exist")(nil)
	require.Error(t, err)
}

func TestDataReadyCheckerAcceptsAWritableDirectory(t *testing.T) {
	err := importarr.DataReadyChecker(t.TempDir())(nil)
	require.NoError(t, err)
}
