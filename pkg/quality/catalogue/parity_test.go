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

package catalogue_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// corpusFormatByTrashID scans testdata/trash/docs/json/<app>/cf for the file
// whose trash_id matches id and returns its raw specifications.
func corpusFormatByTrashID(t *testing.T, app, id string) []struct {
	Implementation string          `json:"implementation"`
	Name           string          `json:"name"`
	Negate         bool            `json:"negate"`
	Required       bool            `json:"required"`
	Fields         json.RawMessage `json:"fields"`
} {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "testdata", "trash", "docs", "json", app, "cf")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		doc, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		var raw struct {
			TrashID        string `json:"trash_id"`
			Specifications []struct {
				Implementation string          `json:"implementation"`
				Name           string          `json:"name"`
				Negate         bool            `json:"negate"`
				Required       bool            `json:"required"`
				Fields         json.RawMessage `json:"fields"`
			} `json:"specifications"`
		}
		require.NoError(t, json.Unmarshal(doc, &raw))
		if raw.TrashID == id {
			return raw.Specifications
		}
	}
	t.Fatalf("no corpus file under %s has trash_id %s", dir, id)
	return nil
}

// TestEveryEmbeddedFormatMatchesItsCorpusSource is the correctness backstop
// the controller amendment requires in place of hand-verifying each
// transcription: for every embedded Format that carries a trash_id, every
// ReleaseTitle/ReleaseGroup condition's compiled Pattern.String() must equal
// the corpus's fields.value for a specification of the same Name, so a
// merged (two-app) Format like web-tier-01 cannot silently drop or duplicate
// a group, and a hand-transcription typo cannot pass silently.
//
// A Format only lists an app in TrashIDs when its embedded Conditions were
// actually verified against that app's own corpus copy; where the two apps'
// copies of the "same" custom format diverge (e.g. web-tier-01's release
// group lists, or vrv's ReleaseTitle pattern differing only in letter case),
// only the app the Conditions were transcribed from is listed -- see
// load_test.go's family tests and the task report for the specific cases.
func TestEveryEmbeddedFormatMatchesItsCorpusSource(t *testing.T) {
	all := loadAllEmbeddedFormats(t) // defined in catalogue_test.go (Step 24)
	for slug, f := range all {
		for app, id := range f.TrashIDs {
			t.Run(slug+"/"+app, func(t *testing.T) {
				corpus := corpusFormatByTrashID(t, app, id)
				for _, cond := range f.Conditions {
					if cond.Pattern == nil {
						continue // non-regex kind, nothing to diff against fields.value
					}
					found := false
					for _, cs := range corpus {
						var fv struct {
							Value string `json:"value"`
						}
						_ = json.Unmarshal(cs.Fields, &fv)
						if cs.Name == cond.Name && fv.Value == cond.Pattern.String() {
							found = true
							break
						}
					}
					require.Truef(t, found, "embedded condition %q on %s has no byte-identical match in the %s corpus file (trash_id %s)", cond.Name, slug, app, id)
				}
			})
		}
	}
}
