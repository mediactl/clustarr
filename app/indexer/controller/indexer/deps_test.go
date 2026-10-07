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

package indexer

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func linked(t *testing.T, pkg string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", pkg).CombinedOutput()
	require.NoError(t, err, "go list -deps %s:\n%s", pkg, out)
	return strings.Fields(string(out))
}

// storage is what only the index agent may link (R8): the release index and
// both its engines.
var storage = []string{
	"modernc.org/sqlite",
	"github.com/jackc/pgx",
	"github.com/mediactl/clustarr/pkg/relindex",
	"github.com/mediactl/clustarr/app/indexer/worker",
}

// managerSide is every indexarr package the manager links, with what each
// must not (spec §4.3 X1-X4, §5.12). `test/guards` (W5.14) overlaps it.
var managerSide = map[string][]string{
	"github.com/mediactl/clustarr/app/indexer/controller/indexer": storage, // X1
	"github.com/mediactl/clustarr/app/indexer/rssschedule":        storage, // X1
	// X2: the wire-client builders and session store the manager's caps probe
	// and logins share with the index agent; nothing of the agent's.
	"github.com/mediactl/clustarr/app/indexer/clients": append(append([]string(nil), storage...),
		"github.com/mediactl/clustarr/app/indexer/download",
		"github.com/mediactl/clustarr/app/indexer/search",
		"github.com/mediactl/clustarr/app/indexer/query",
		"github.com/mediactl/clustarr/app/indexer/facade",
		"github.com/mediactl/clustarr/app/indexer/controller",
		"github.com/mediactl/clustarr/app/indexer/clientcache",
	),
}

func TestIndexerManagerSideLinksNoAgentCode(t *testing.T) {
	for pkg, denied := range managerSide {
		t.Run(pkg[strings.LastIndex(pkg, "/")+1:], func(t *testing.T) {
			for _, dep := range linked(t, pkg) {
				for _, bad := range denied {
					if dep == bad || strings.HasPrefix(dep, bad+"/") {
						assert.Failf(t, "links agent code", "%s links %s", pkg, dep)
					}
				}
			}
		})
	}
}
