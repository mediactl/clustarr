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

package cardigann_test

import (
	"os"
	"path/filepath"
	"testing"
)

// readTestdata reads a file from test/data/cardigann/ (repo-root, two levels
// up from this package) and fails the test on any read error. Every _test.go
// file in this package uses it, and its byte-identical twin readTestdataBytes,
// to load both YAML definitions and the binary/HTML/JSON response fixtures.
func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "test", "data", "cardigann", name))
	if err != nil {
		t.Fatalf("cardigann: read testdata %q: %v", name, err)
	}
	return data
}

// readTestdataBytes is readTestdata under the name used where a fixture is
// served as a raw HTTP response body rather than decoded as a definition —
// kept as a distinct name only for readability at call sites, same function.
func readTestdataBytes(t *testing.T, name string) []byte { return readTestdata(t, name) }
