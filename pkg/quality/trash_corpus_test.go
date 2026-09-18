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

package quality_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/stretchr/testify/require"
)

type corpusSpec struct {
	Implementation string          `json:"implementation"`
	Fields         json.RawMessage `json:"fields"`
}
type corpusFormat struct {
	Specifications []corpusSpec `json:"specifications"`
}

// regexBearingImplementations are the *arr specification types whose
// fields.value is a regex string (docs/research/quality.md §4.1's table).
var regexBearingImplementations = map[string]bool{
	"ReleaseTitleSpecification": true,
	"ReleaseGroupSpecification": true,
	"EditionSpecification":      true, // accepted no-op in Match, but still a regex in the corpus
}

// TestEveryVendoredTRaSHRegexCompilesUnderRegexp2 is the gate CLAUDE.md's
// "157 of 2791 patterns" gotcha exists for, run for real against the
// corpus vendored by hack/sync-trash.sh at the commit recorded in
// testdata/trash/COMMIT (3f532266d93ae0fa9c65cd2efcb503ead7ce7784,
// 2026-09-18), instead of asserted from the research note.
func TestEveryVendoredTRaSHRegexCompilesUnderRegexp2(t *testing.T) {
	var total int
	for _, app := range []string{"radarr", "sonarr"} {
		dir := filepath.Join("..", "..", "testdata", "trash", "docs", "json", app, "cf")
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		for _, e := range entries {
			doc, err := os.ReadFile(filepath.Join(dir, e.Name()))
			require.NoError(t, err)
			var cf corpusFormat
			require.NoError(t, json.Unmarshal(doc, &cf))
			for _, spec := range cf.Specifications {
				if !regexBearingImplementations[spec.Implementation] {
					continue
				}
				var fields struct {
					Value string `json:"value"`
				}
				require.NoError(t, json.Unmarshal(spec.Fields, &fields))
				re, err := regexp2.Compile(fields.Value, regexp2.IgnoreCase)
				require.NoErrorf(t, err, "%s/%s: pattern %q failed to compile", app, e.Name(), fields.Value)
				re.MatchTimeout = 50 * time.Millisecond
				total++
			}
		}
	}
	// Count observed at the pinned commit 3f532266d93ae0fa9c65cd2efcb503ead7ce7784
	// (see testdata/trash/COMMIT), recorded here after this test's first real
	// run against the vendored corpus -- 2791, matching
	// docs/research/quality.md's figure exactly at this commit.
	require.Equal(t, 2791, total)
}
