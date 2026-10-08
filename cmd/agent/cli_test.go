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
	"io"
	"os"
	"testing"

	"github.com/mediactl/clustarr/internal/cli/agent"
)

// parse runs the agent's real command tree with a recording RunFunc and
// returns the options the command line produced. POD_NAMESPACE is what the
// downward API sets; the index domain's facade needs it. It is read when
// the command is built, so a test sets the environment before calling parse.
func parse(t *testing.T, args ...string) (agent.Options, error) {
	t.Helper()
	if _, ok := os.LookupEnv("POD_NAMESPACE"); !ok {
		t.Setenv("POD_NAMESPACE", "default")
	}
	var got agent.Options
	cmd := agent.NewCommandWith(func(_ context.Context, o agent.Options) error { got = o; return nil })
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	return got, cmd.Execute()
}
