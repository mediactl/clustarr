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

package main_test

import (
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	indexarr "github.com/mediactl/clustarr/app/indexer"
)

// Both of these constants shipped disagreeing with the manifest, and neither
// disagreement could fail anything: DefaultIndexPath pointed at /index, which
// readOnlyRootFilesystem makes unwritable, and DefaultFacadeBindAddress bound
// Prowlarr's 9696 while the Service routes 8080. A deployed pod takes the env
// var and the containerPort, so the compiled-in values were only ever reached
// by someone running the binary directly -- which is exactly the person least
// equipped to diagnose a read-only filesystem or an unroutable port.
//
// These tests PARSE the manifest rather than restating its values. A test that
// restates the value it guards cannot catch drift; that is how ValidKVKey
// shipped permissive in Phase C, pinned against a regex copied into the test
// file instead of against the server that rejects the key.
const indexarrManifest = "../../config/manager/indexarr.yaml"

func TestDefaultIndexPathMatchesTheManifest(t *testing.T) {
	b, err := os.ReadFile(indexarrManifest)
	require.NoError(t, err)

	m := regexp.MustCompile(`(?m)^\s*-\s*name:\s*CLUSTARR_INDEX_PATH\s*\n\s*value:\s*(\S+)\s*$`).
		FindSubmatch(b)
	require.Len(t, m, 2, "CLUSTARR_INDEX_PATH is not set in %s -- if it moved, this test must follow it", indexarrManifest)

	assert.Equal(t, string(m[1]), indexarr.DefaultIndexPath,
		"the compiled-in index path and the manifest's CLUSTARR_INDEX_PATH disagree; "+
			"the pod runs readOnlyRootFilesystem, so a path outside the PVC mount cannot be written")
}

func TestDefaultFacadeBindAddressMatchesTheServicePort(t *testing.T) {
	b, err := os.ReadFile(indexarrManifest)
	require.NoError(t, err)

	m := regexp.MustCompile(`(?m)^\s*-\s*name:\s*http\s*\n\s*containerPort:\s*(\d+)\s*$`).
		FindSubmatch(b)
	require.Len(t, m, 2, "no named http port in %s -- if it was renamed, this test must follow it", indexarrManifest)

	assert.Equal(t, ":"+string(m[1]), indexarr.DefaultFacadeBindAddress,
		"the facade would bind a port the Service does not route to, so it would be unreachable")
}
