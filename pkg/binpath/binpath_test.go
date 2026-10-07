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

package binpath_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/binpath"
)

// No image sets an ENTRYPOINT (spec §10.1.1), so every container names one of
// these. Each is /usr/bin/<binary>, and no two are the same.
func TestEveryBinaryLivesInUsrBinUnderItsOwnName(t *testing.T) {
	seen := map[string]bool{}
	for _, tc := range []struct{ path, name string }{
		{binpath.Manager, "manager"},
		{binpath.UI, "ui"},
		{binpath.Agent, "agent"},
		{binpath.Markers, "markers"},
		{binpath.Transcode, "transcode"},
	} {
		assert.Equal(t, "/usr/bin/"+tc.name, tc.path)
		assert.False(t, seen[tc.path], "%s named twice", tc.path)
		seen[tc.path] = true
	}
}
