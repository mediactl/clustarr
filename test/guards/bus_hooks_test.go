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
	"go/ast"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEveryBusConnectionCarriesTraceHooks: every bus connection in
// production code carries the trace hooks -- busconn.Connect with
// busconn.WithHooks(obs.BusHooks()), and natsbus.New (markers and
// transcode, which must not link pkg/obs) with the inline
// natsbus.WithHooks(events.Hooks{BeforePublish: tracing.Inject,
// AfterReceive: tracing.Extract}). pkg/busconn's own natsbus.New is the
// implementation of Connect and is exempt. Discovered over every production
// file, not listed (split design §10.3.3, W5.15).
//
// pkg/events keeps its hooks as plain function fields so the bus layer never
// depends on the observability layer, so the hooks are something each
// process passes in -- and therefore something a new call site can forget.
// That is how Clustarr once shipped a complete W3C propagation in
// pkg/events, a matching pair of hooks in pkg/obs, and not one production
// call site that joined them. A missing hook is silence, not an error.
func TestEveryBusConnectionCarriesTraceHooks(t *testing.T) {
	var connects, news int
	for p, f := range productionFiles(t) {
		busconnAlias := importAlias(f, mod+"pkg/busconn", "busconn")
		obsAlias := importAlias(f, mod+"pkg/obs", "obs")
		natsbusAlias := importAlias(f, mod+"pkg/events/natsbus", "natsbus")
		tracingAlias := importAlias(f, mod+"pkg/obs/tracing", "tracing")
		eventsAlias := importAlias(f, mod+"pkg/events", "events")
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch {
			case busconnAlias != "" && isSelector(call.Fun, busconnAlias, "Connect"):
				connects++
				assert.True(t, hasCall(call.Args, busconnAlias, "WithHooks", func(c *ast.CallExpr) bool {
					return hasCall(c.Args, obsAlias, "BusHooks", nil)
				}), "%s: busconn.Connect without busconn.WithHooks(obs.BusHooks()): this process publishes and consumes with no trace propagation, silently", p)
			case natsbusAlias != "" && isSelector(call.Fun, natsbusAlias, "New") && !strings.HasPrefix(p, "pkg/busconn/"):
				news++
				assert.True(t, hasCall(call.Args, natsbusAlias, "WithHooks", func(c *ast.CallExpr) bool {
					return inlineTraceHooks(c, eventsAlias, tracingAlias)
				}), "%s: natsbus.New without natsbus.WithHooks(events.Hooks{BeforePublish: tracing.Inject, AfterReceive: tracing.Extract})", p)
			}
			return true
		})
	}
	require.GreaterOrEqual(t, connects, 3, "found %d busconn.Connect calls; the manager, the agent and the ui connect through busconn", connects)
	require.GreaterOrEqual(t, news, 2, "found %d natsbus.New calls; markers and transcode build their bus by hand", news)
}

// inlineTraceHooks reports whether c's one argument is an
// events.Hooks{...} literal with BeforePublish: tracing.Inject and
// AfterReceive: tracing.Extract.
func inlineTraceHooks(c *ast.CallExpr, eventsAlias, tracingAlias string) bool {
	if len(c.Args) != 1 || tracingAlias == "" {
		return false
	}
	lit, ok := c.Args[0].(*ast.CompositeLit)
	if !ok || !isSelector(lit.Type, eventsAlias, "Hooks") {
		return false
	}
	var inject, extract bool
	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		switch {
		case isIdent(kv.Key, "BeforePublish") && isSelector(kv.Value, tracingAlias, "Inject"):
			inject = true
		case isIdent(kv.Key, "AfterReceive") && isSelector(kv.Value, tracingAlias, "Extract"):
			extract = true
		}
	}
	return inject && extract
}
