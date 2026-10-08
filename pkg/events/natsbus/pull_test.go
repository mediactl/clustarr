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

package natsbus_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/contracttest"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

// TestPullWritesTheSubscriptionsCapNotItsSlots: a pool durable is created by
// Pull, and its cap across every puller is the topology's although each pool
// worker has one slot (spec §9.3); its dead-letter watcher is created beside
// it, since no topology declares a pool durable.
func TestPullWritesTheSubscriptionsCapNotItsSlots(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bus, err := natsbus.New(connect(t))
	if err != nil {
		t.Fatalf("natsbus.New: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	if err := bus.Ensure(ctx, contracttest.Topology()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	spec := events.TranscodeTaskConsumer("prof-cap", "cpu")
	sub := spec.Subscription()
	if sub.MaxInFlight >= spec.MaxAckPending {
		t.Fatalf("the pool consumer's slots (%d) are not below its cap (%d): the test cannot tell them apart",
			sub.MaxInFlight, spec.MaxAckPending)
	}
	p, err := bus.Pull(ctx, sub)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	defer p.Stop()
	c, err := bus.JetStream().Consumer(ctx, spec.Stream, spec.Name)
	if err != nil {
		t.Fatalf("consumer %s: %v", spec.Name, err)
	}
	if got := c.CachedInfo().Config.MaxAckPending; got != spec.MaxAckPending {
		t.Errorf("pool durable MaxAckPending = %d, want the topology's %d, not the worker's %d slot(s)",
			got, spec.MaxAckPending, sub.MaxInFlight)
	}
	watcher := events.DeadLetterWatcherName(spec.Stream, spec.Name)
	if _, err := bus.JetStream().Consumer(ctx, events.StreamAdvisories, watcher); err != nil {
		t.Errorf("Pull did not create the pool durable's dead-letter watcher %s: %v", watcher, err)
	}
}

// TestOnlyPullWritesConsumerConfig holds natsbus to spec §5.9: EnsureTopology
// (pkg/events) is the only writer of a static durable's config, and Subscribe
// and the dead-letter watchers bind. The natsbus paths that create a consumer
// and its watcher are Pull, whose pool durables come and go with
// TranscodeProfiles, and EnsureConsumer, the manager's for a dynamic engine
// durable (A1.6, ADR-0019 §8.5, ruling R15).
func TestOnlyPullWritesConsumerConfig(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	writes := map[string]bool{"CreateOrUpdateConsumer": true, "CreateConsumer": true, "UpdateConsumer": true}
	allowed := map[string]bool{"Pull": true, "EnsureConsumer": true}
	fset := token.NewFileSet()
	var where []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && writes[sel.Sel.Name] {
					where = append(where, fn.Name.Name+" "+fset.Position(call.Pos()).String())
				}
				return true
			})
		}
	}
	if len(where) == 0 {
		t.Fatal("no consumer write found at all: Pull must create its durable; this guard is not looking where it thinks it is")
	}
	for _, w := range where {
		fn, pos, _ := strings.Cut(w, " ")
		if !allowed[fn] {
			t.Errorf("%s (in %s) writes consumer config outside Pull and EnsureConsumer", pos, fn)
		}
	}
}
