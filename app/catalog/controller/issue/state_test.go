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
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/issue"
)

func TestState(t *testing.T) {
	cases := []struct {
		name        string
		monitored   bool
		hasFile     bool
		cutoffMet   bool
		downloading bool
		pendingGrab bool
		released    bool
		want        commonv1.LeafPhase
	}{
		{"unmonitored outranks everything", false, true, true, true, true, true, catalogv1alpha1.IssuePhaseUnmonitored},
		{"an active download outranks a file below the cutoff", true, true, false, true, false, true, catalogv1alpha1.IssuePhaseDownloading},
		{"a file alone below the cutoff", true, true, false, false, false, true, catalogv1alpha1.IssuePhaseCutoffUnmet},
		{"an active download with no file yet", true, false, false, true, false, true, catalogv1alpha1.IssuePhaseDownloading},
		{"nothing yet", true, false, false, false, false, true, catalogv1alpha1.IssuePhaseWanted},
		// X15: status.pendingGrab reads delayed, ranked as book.Phase ranks it.
		{"a delayed grab with no file", true, false, false, false, true, true, catalogv1alpha1.IssuePhaseDelayed},
		{"a delayed upgrade of a file below the cutoff", true, true, false, false, true, true, catalogv1alpha1.IssuePhaseDelayed},
		{"a file meeting the cutoff outranks a delayed grab", true, true, true, false, true, true, catalogv1alpha1.IssuePhaseImported},
		{"the Download a delayed grab became outranks it", true, false, false, true, true, true, catalogv1alpha1.IssuePhaseDownloading},
		// 2026-11-10 (design D4): an unreleased issue with nothing on disk or
		// in flight reads Unavailable, the Episode's Unaired.
		{"unreleased with nothing yet", true, false, false, false, false, false, catalogv1alpha1.IssuePhaseUnavailable},
		{"unreleased but a file arrived early", true, true, true, false, false, false, catalogv1alpha1.IssuePhaseImported},
		{"unreleased with a download in flight", true, false, false, true, false, false, catalogv1alpha1.IssuePhaseDownloading},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, issue.State(c.monitored, c.hasFile, c.cutoffMet, c.downloading, c.pendingGrab, c.released))
		})
	}
}
