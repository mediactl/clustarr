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

package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The process's service.name, shared by its records and its spans (design
// §3): agent-<domain>, and engine-<DownloadClient> for the two engine
// domains.
func TestAgentServiceNames(t *testing.T) {
	for _, tc := range []struct{ domain, engine, want string }{
		{"catalog", "", "agent-catalog"},
		{"import", "", "agent-import"},
		{"torrent-engine", "qbt", "engine-qbt"},
		{"usenet-engine", "nzb-0", "engine-nzb-0"},
	} {
		require.Equal(t, tc.want, agentServiceName(tc.domain, tc.engine), "%s/%s", tc.domain, tc.engine)
	}
}
