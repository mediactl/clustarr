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
	"os"
	"testing"

	"github.com/mediactl/clustarr/pkg/par2child"
)

// TestMain dispatches the usenet engine's repair child before any test runs:
// the child is os.Executable(), which in a test is this test binary, and
// without the dispatch the engine's start probe would run the package's
// tests again as its own child (pkg/par2child.RunIfChild).
func TestMain(m *testing.M) {
	par2child.RunIfChild()
	os.Exit(m.Run())
}
