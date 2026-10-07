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

package conditions_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mediactl/clustarr/pkg/k8s/conditions"
)

func TestReadersFindAConditionByType(t *testing.T) {
	conds := []metav1.Condition{
		{Type: conditions.ConditionReady, Status: metav1.ConditionTrue, ObservedGeneration: 4},
		{Type: "Healthy", Status: metav1.ConditionFalse, ObservedGeneration: 3},
		{Type: "Probing", Status: metav1.ConditionUnknown, ObservedGeneration: 2},
	}
	for _, tc := range []struct {
		name                 string
		condType             string
		found, isTrue, isFal bool
		gen                  int64
	}{
		{"true", conditions.ConditionReady, true, true, false, 4},
		{"false", "Healthy", true, false, true, 3},
		{"unknown", "Probing", true, false, false, 2},
		{"absent", "Nope", false, false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.found, conditions.FindCondition(conds, tc.condType) != nil)
			assert.Equal(t, tc.isTrue, conditions.IsConditionTrue(conds, tc.condType))
			assert.Equal(t, tc.isFal, conditions.IsConditionFalse(conds, tc.condType))
			assert.Equal(t, tc.gen, conditions.ObservedGeneration(conds, tc.condType))
		})
	}
	assert.True(t, conditions.IsReady(conds))
	assert.False(t, conditions.IsReady(nil))
}

// A stale Ready=True left from an earlier generation is the classic way to
// miss a regression; StatusUpToDate is the question that catches it.
func TestStatusUpToDate(t *testing.T) {
	conds := []metav1.Condition{{Type: conditions.ConditionReady, Status: metav1.ConditionTrue, ObservedGeneration: 4}}
	for _, tc := range []struct {
		name     string
		obj      metav1.Object
		condType string
		want     bool
	}{
		{"set for this generation", &metav1.ObjectMeta{Generation: 4}, conditions.ConditionReady, true},
		{"stale after a spec edit", &metav1.ObjectMeta{Generation: 5}, conditions.ConditionReady, false},
		{"missing condition", &metav1.ObjectMeta{Generation: 4}, "Nonexistent", false},
		{"nil object", nil, conditions.ConditionReady, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, conditions.StatusUpToDate(tc.obj, conds, tc.condType))
		})
	}
}
