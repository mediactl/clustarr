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

package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/ui"
)

// TestUIHandsTheChosenAuthModeToRunUI pins §A3.5's explicit
// authentication mode where the process chooses it: `ui --auth-mode` (the
// `clustarr all --ui-auth-mode` leg went with `clustarr all`, §3.10). The
// flag has no default, so a bare command hands its run options whose
// Validate -- the check ui.Run makes before it binds -- refuses to serve and
// says which flag to set.
func TestUIHandsTheChosenAuthModeToRunUI(t *testing.T) {
	useUnreachableKubeconfig(t)

	for name, tc := range map[string]struct {
		argv     []string
		wantMode ui.AuthMode
	}{
		"ui --auth-mode anonymous": {
			argv:     []string{"--bind-address", "127.0.0.1:0", "--auth-mode", "anonymous"},
			wantMode: ui.AuthModeAnonymous,
		},
		"ui, no mode": {
			argv: []string{"--bind-address", "127.0.0.1:0"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			o := captureUIOptions(t, tc.argv...)
			require.Equal(t, tc.wantMode, o.AuthMode)
			if tc.wantMode == "" {
				err := o.Validate()
				require.Error(t, err, "a ui started without an authentication mode must refuse to serve")
				require.Contains(t, err.Error(), "--auth-mode")
				return
			}
			require.NoError(t, o.Validate())
		})
	}
}
