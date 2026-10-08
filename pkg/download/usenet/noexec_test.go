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

package usenet

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTheUsenetClientExecsNothing: the client repairs through a Repairer;
// the one exec is pkg/par2child's (§8.9 guard 3; also held repo-wide,
// §4.5.6).
func TestTheUsenetClientExecsNothing(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	checked := 0
	for _, p := range files {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		require.NoError(t, err)
		checked++
		for _, imp := range f.Imports {
			if imp.Path.Value == `"os/exec"` {
				t.Errorf("%s imports os/exec: repair goes through a Repairer, and the one exec is pkg/par2child's", p)
			}
		}
	}
	require.Positive(t, checked, "no production file was checked")
}
