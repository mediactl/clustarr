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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mediactl/clustarr/catalogarr/controller/issue"
)

func TestCutoffCondition(t *testing.T) {
	cases := []struct {
		name       string
		hasFile    bool
		cutoffMet  bool
		problem    string
		wantStatus metav1.ConditionStatus
		wantReason string
	}{
		{"no file", false, false, "", metav1.ConditionFalse, "NoFile"},
		{"no file outranks an unresolved profile", false, false, "comic \"x\" not found", metav1.ConditionFalse, "NoFile"},
		{"file, profile unresolved", true, false, "qualityProfile \"p\" not found", metav1.ConditionFalse, "ProfileUnresolved"},
		{"file meets the cutoff", true, true, "", metav1.ConditionTrue, "CutoffMet"},
		{"file below the cutoff", true, false, "", metav1.ConditionFalse, "BelowCutoff"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status, reason, message := issue.CutoffCondition(c.hasFile, c.cutoffMet, c.problem)
			assert.Equal(t, c.wantStatus, status)
			assert.Equal(t, c.wantReason, reason)
			assert.NotEmpty(t, message)
		})
	}
}
