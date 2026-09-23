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

package ui_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui"
)

// TestOptionsValidateRequiresAnExplicitAuthMode pins the rule §A3.5 now
// states: the ui serves nothing until an operator has chosen its
// authentication mode. Anonymous is the only mode, and it must still be
// named -- an empty mode is a refusal, not a default -- so a bare
// `clustarr ui` cannot expose the whole library by omission.
func TestOptionsValidateRequiresAnExplicitAuthMode(t *testing.T) {
	for name, tc := range map[string]struct {
		mode    ui.AuthMode
		wantErr []string
	}{
		"unset":     {mode: "", wantErr: []string{"--auth-mode", "anonymous"}},
		"unknown":   {mode: "basic", wantErr: []string{"--auth-mode", `"basic"`, "anonymous"}},
		"anonymous": {mode: ui.AuthModeAnonymous},
	} {
		t.Run(name, func(t *testing.T) {
			err := ui.Options{AuthMode: tc.mode}.Validate()
			if len(tc.wantErr) == 0 {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, want := range tc.wantErr {
				require.Contains(t, err.Error(), want,
					"the error must name the flag and the one mode that exists")
			}
		})
	}
}

// TestRunRefusesToServeWithoutAnAuthMode is the behavioural half: Run
// returns Validate's error before it binds, so a process started without
// --auth-mode exits at once and never answers a request.
func TestRunRefusesToServeWithoutAnAuthMode(t *testing.T) {
	addr := freeAddr(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := ui.Run(ctx, ui.Options{BindAddress: addr})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--auth-mode")
	require.NoError(t, ctx.Err(), "Run must refuse at once, not wait for its context")

	conn, dialErr := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if dialErr == nil {
		_ = conn.Close()
		t.Fatalf("ui.Run bound %s despite refusing to serve", addr)
	}
}

// TestNewServerNamesTheConfiguredAuthMode: the one line the ui logs outside
// a request says which mode it runs under and what that means, so an
// operator reading the pod's first lines sees "anonymous" and the
// ingress-authentication requirement together.
func TestNewServerNamesTheConfiguredAuthMode(t *testing.T) {
	var buf bytes.Buffer
	ctx := logging.NewContext(t.Context(), slog.New(slog.NewJSONHandler(&buf, nil)))

	ui.NewServer(ctx, ui.Options{AuthMode: ui.AuthModeAnonymous})

	require.Contains(t, buf.String(), `"WARN"`, "anonymous access stays a warning, not an info line")
	require.Contains(t, buf.String(), "anonymous")
	require.Contains(t, buf.String(), "ingress authentication")
}
