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

package task

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/transcode"
)

func TestTightestKeepsTheFewestMeasuredVideoEngines(t *testing.T) {
	two := transcode.Limits{VideoEngines: 2}
	one := transcode.Limits{VideoEngines: 1}
	assert.Equal(t, 1, tightest(two, one).VideoEngines)
	assert.Equal(t, 2, tightest(two, transcode.Limits{}).VideoEngines, "an unmeasured side keeps the other's count")
	assert.Equal(t, 0, tightest(transcode.Limits{}, transcode.Limits{}).VideoEngines)
}
