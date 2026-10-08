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
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/mediactl/clustarr/internal/cli/manager"
	"github.com/mediactl/clustarr/pkg/version"
)

// execute runs the manager's command tree with a run that starts nothing
// and returns its output.
func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	root := manager.NewCommandWith(func(context.Context, manager.Options) error { return nil })
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestVersionCommand(t *testing.T) {
	out, err := execute(t, "version")
	if err != nil {
		t.Fatalf("manager version: %v (output %q)", err, out)
	}
	if !strings.Contains(out, "manager "+version.String()) {
		t.Errorf("output %q does not contain %q", out, "manager "+version.String())
	}
	if !strings.Contains(out, runtime.Version()) {
		t.Errorf("output %q does not name the Go toolchain", out)
	}
}

func TestVersionCommandTakesNoArguments(t *testing.T) {
	if _, err := execute(t, "version", "extra"); err == nil {
		t.Fatal("manager version accepted a positional argument")
	}
}
