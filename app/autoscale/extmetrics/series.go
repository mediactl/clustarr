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

package extmetrics

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/mediactl/clustarr/pkg/agentdomain"
	"github.com/mediactl/clustarr/pkg/events"
)

// ConsumerStater reads one durable's backlog; events.StreamAdmin is one.
type ConsumerStater interface {
	ConsumerState(ctx context.Context, stream, durable string) (events.ConsumerState, error)
}

// Series is one servable (stream, consumer) pair.
type Series struct{ Stream, Consumer string }

// ServableSeries is every consumer of every autoscaled domain, with its
// stream, sorted (§9.4: exactly these are served).
func ServableSeries(domains []agentdomain.Domain, t events.Topology) ([]Series, error) {
	var out []Series
	for _, d := range domains {
		if !d.Autoscaled {
			continue
		}
		for _, name := range d.Consumers {
			c, ok := t.Consumer(name)
			if !ok {
				return nil, fmt.Errorf("extmetrics: domain %s names consumer %s, which the topology does not declare", d.Name, name)
			}
			out = append(out, Series{Stream: c.Stream, Consumer: c.Name})
		}
	}
	slices.SortFunc(out, func(a, b Series) int {
		return cmp.Or(cmp.Compare(a.Stream, b.Stream), cmp.Compare(a.Consumer, b.Consumer))
	})
	return out, nil
}
