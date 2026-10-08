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

package testmedia_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testmedia is test support: no binary links it (upgrade guide U2.1).
func TestNoBinaryLinksTestmedia(t *testing.T) {
	for _, bin := range []string{"manager", "ui", "agent", "markers", "transcode"} {
		out, err := exec.Command("go", "list", "-deps", "github.com/mediactl/clustarr/cmd/"+bin).Output()
		require.NoError(t, err, bin)
		assert.NotContains(t, strings.Fields(string(out)), "github.com/mediactl/clustarr/internal/testmedia", "cmd/%s links testmedia", bin)
	}
}
