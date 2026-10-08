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

package uicli

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/grab/engine"
)

// TestTransferProgressKeyIsTheEngines pins the ui's restated transfer
// telemetry key to the one the grab engines write under: the ui restates it
// so cmd/ui links no app/ package (spec §4.5.2), and only this test keeps
// the two from drifting apart -- a drift would leave every Downloads row
// without its rate and ETA, and nothing else would fail.
func TestTransferProgressKeyIsTheEngines(t *testing.T) {
	for _, uid := range []string{"0b7c1d5e-8f2a-4c3b-9d1e-6a5f4b3c2d1e", "a.b", "with space", ""} {
		require.Equal(t, engine.ProgressKey(uid), transferProgressKey(uid), "uid %q", uid)
	}
}
