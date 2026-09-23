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

package issue_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/issue"
)

func TestState(t *testing.T) {
	cases := []struct {
		name        string
		monitored   bool
		hasFile     bool
		downloading bool
		want        catalogv1alpha1.IssueState
	}{
		{"unmonitored outranks everything", false, true, true, catalogv1alpha1.IssueStateSkipped},
		{"a file outranks an active download", true, true, true, catalogv1alpha1.IssueStateDownloaded},
		{"a file alone", true, true, false, catalogv1alpha1.IssueStateDownloaded},
		{"an active download with no file yet", true, false, true, catalogv1alpha1.IssueStateSnatched},
		{"nothing yet", true, false, false, catalogv1alpha1.IssueStateWanted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, issue.State(c.monitored, c.hasFile, c.downloading))
		})
	}
}
