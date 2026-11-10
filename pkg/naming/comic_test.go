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

package naming_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/naming"
)

func TestIssueFile(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	c := naming.Context{ComicSeriesTitle: "Saga", IssueNumber: "001"}
	got, err := e.IssueFile(c)
	require.NoError(t, err)
	require.Equal(t, "Saga/Saga c001", got)
}

// TestIssueFileNamesTheSpecialVersion: a collected edition's issue carries
// the Comic's special version after its number, Kapowarr's special_version
// (2026-11-10 design D7, D10); a normal run renders exactly as before.
func TestIssueFileNamesTheSpecialVersion(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	for _, tc := range []struct{ version, want string }{
		{"", "Saga/Saga c001"},
		{"TPB", "Saga/Saga c001 TPB"},
		{"One-Shot", "Saga/Saga c001 One-Shot"},
		{"Hard Cover", "Saga/Saga c001 Hard Cover"},
		{"Omnibus", "Saga/Saga c001 Omnibus"},
	} {
		got, err := e.IssueFile(naming.Context{ComicSeriesTitle: "Saga", IssueNumber: "001", SpecialVersion: tc.version})
		require.NoError(t, err, tc.version)
		require.Equal(t, tc.want, got, tc.version)
	}
}
