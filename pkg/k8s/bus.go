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

package k8s

import (
	"github.com/mediactl/clustarr/pkg/events"
)

// The bus connector lives in pkg/busconn (spec §4.3 step 1.3), which cmd/ui
// links without pkg/k8s. Only the topology choice stays here, because it
// reads Options.

// BusTopology is the topology this process should install: the default from
// pkg/events, collapsed to a single replica when [Options.BusSingleNode] is
// set.
func (o Options) BusTopology() events.Topology {
	t := events.Default()
	if o.BusSingleNode {
		return t.ForSingleNode()
	}
	return t
}
