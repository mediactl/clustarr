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
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/internal/cli"
	uicli "github.com/mediactl/clustarr/internal/cli/ui"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
)

// unreachableKubeconfig is a well-formed kubeconfig naming a server nothing
// listens on: enough for config.GetConfig and a lazy client, never contacted.
const unreachableKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster: {server: "https://127.0.0.1:1"}
contexts:
- name: c
  context: {cluster: c, user: u}
current-context: c
users:
- name: u
  user: {token: x}
`

// useUnreachableKubeconfig points KUBECONFIG at unreachableKubeconfig.
func useUnreachableKubeconfig(t *testing.T) {
	t.Helper()
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(kubeconfig, []byte(unreachableKubeconfig), 0o600))
	t.Setenv("KUBECONFIG", kubeconfig)
}

// TestUIWiresEveryUIOption (the loop spec's TestBothUICommandsWireEveryUIOption,
// renamed with the binary split, W5.11) is the behavioural half of
// wiring_projection_test.go. That test reads internal/cli/ui as text, which
// is cheap and names the gap precisely, but a text match passes for a field
// that is named and never reaches the run -- F-6 found exactly that shape.
// This one EXECUTES `ui` (internal/cli/ui; `clustarr all` is gone, §3.10)
// with a recording run and inspects the ui.Options it actually hands over.
//
// It asserts, on the value itself:
//
//   - every func, pointer and interface field of ui.Options is set. Each is
//     legal to leave nil -- ui.NewServer defaults every one to "no rows" or a
//     per-connection poll, and a nil Actions answers every button with
//     actions.ErrNoWriter -- so an unset field renders a working-looking page
//     and fails nothing else in the tree. That is how Options.Actions stayed
//     unset from G3-1 to G3-5 while every monitor, search, rescan, assign and
//     settings form returned 503. Walking the struct by reflection means a
//     field added later is covered the day it appears;
//   - every field named after a *projection.Projection method is that
//     method's value, so the pages and streams share the one projection loop
//     (design plan ruling R4) instead of each polling on its own;
//   - Actions carries a real writer: an empty request comes back
//     actions.ErrInvalid, which Actions only reaches past its no-writer check.
//
// The kubeconfig names an apiserver nothing listens on: config.GetConfig
// only parses it, and building ui's cache, projection and action writer
// dials nothing, so the command reaches its run exactly as it would against
// a real cluster. With no kubeconfig at all, a nil Reader and a nil Actions
// are the correct result rather than a wiring gap, which is why this test
// supplies one.
//
// A3.9 names two of the fields the walk covers: TransferProgress and
// ImportDetail, the Downloads page's reads of clustarr-progress and
// clustarr-imports over the read-only bus, which replaced the Download List.
// Both are asserted by name as well, so the walk's premise (that a nil func
// field is a wiring gap) cannot be loosened for them without this test
// saying so.
func TestUIWiresEveryUIOption(t *testing.T) {
	useUnreachableKubeconfig(t)
	o := captureUIOptions(t, "--bind-address", "127.0.0.1:0", "--auth-mode", "anonymous")
	requireEveryUIOptionWired(t, "ui", o)
	require.NotNil(t, o.TransferProgress, "ui.Options.TransferProgress: the Downloads page's transfer telemetry (A3.9)")
	require.NotNil(t, o.ImportDetail, "ui.Options.ImportDetail: an entry's import records (A3.9)")
}

// uicliCommandRecording is the ui command with run in place of ui.Run.
func uicliCommandRecording(run uicli.RunFunc) *cobra.Command { return uicli.NewCommandWith(run) }

// captureUIOptions executes the ui command with argv and a recording run
// and returns the ui.Options the run was called with. The command's context
// is cancelled when the test ends, which stops the cache and projection
// goroutines the command started.
func captureUIOptions(t *testing.T, argv ...string) ui.Options {
	t.Helper()
	o, err := runUICommand(t, argv...)
	require.NoError(t, err, "ui %s", strings.Join(argv, " "))
	return o
}

// runUICommand executes the ui command with argv and a recording run and
// returns the options the run saw and the command's error.
func runUICommand(t *testing.T, argv ...string) (ui.Options, error) {
	t.Helper()
	var (
		mu     sync.Mutex
		got    ui.Options
		called bool
	)
	root := uicliCommandRecording(func(_ context.Context, o ui.Options) error {
		mu.Lock()
		defer mu.Unlock()
		got, called = o, true
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	root.SetArgs(argv)
	err := root.ExecuteContext(ctx)

	mu.Lock()
	defer mu.Unlock()
	if err == nil {
		require.True(t, called, "ui %s never called its run", strings.Join(argv, " "))
	}
	return got, err
}

// requireEveryUIOptionWired is TestUIWiresEveryUIOption's assertion.
func requireEveryUIOptionWired(t *testing.T, command string, o ui.Options) {
	t.Helper()

	projMethods := map[string]bool{}
	pt := reflect.TypeOf(&projection.Projection{})
	for i := range pt.NumMethod() {
		projMethods[pt.Method(i).Name] = true
	}

	v := reflect.ValueOf(o)
	for i := range v.NumField() {
		field := v.Type().Field(i)
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.Func, reflect.Pointer, reflect.Interface:
		default:
			continue
		}
		if fv.IsNil() {
			t.Errorf("%s hands runUI a nil ui.Options.%s. A nil field is legal and silently "+
				"degrades -- no rows, a per-connection poll, or (Actions) a 503 on every button -- "+
				"so nothing else in the tree goes red. Wire it in internal/cli/ui.",
				command, field.Name)
			continue
		}
		if fv.Kind() == reflect.Func && projMethods[field.Name] {
			name := runtime.FuncForPC(fv.Pointer()).Name()
			want := "ui/projection.(*Projection)." + field.Name + "-fm"
			if !strings.HasSuffix(name, want) {
				t.Errorf("%s sets ui.Options.%s to %s, not the shared projection's own %s method: "+
					"every page and stream must come from the one projection loop (ruling R4)",
					command, field.Name, name, field.Name)
			}
		}
	}

	if o.Actions != nil {
		_, err := o.Actions.SetMonitored(context.Background(), "", commonv1alpha1.MediaKindMovie, "", true)
		require.ErrorIs(t, err, actions.ErrInvalid,
			"%s hands runUI an *actions.Actions with no writer behind it: every action would "+
				"return ErrNoWriter. Build it with actions.New over a client.Client.", command)
	}
}

// The ui command signs Plex's photo URLs with the key the installer puts in
// $CLUSTARR_ART_SIGNING_KEY, so the URLs Plex stores outlive a restart.
func TestUIReadsTheArtSigningKey(t *testing.T) {
	useUnreachableKubeconfig(t)
	key := strings.Repeat("0123456789abcdef", 4)
	t.Setenv(cli.EnvArtSigningKey, key)
	require.Equal(t, []byte(key),
		captureUIOptions(t, "--bind-address", "127.0.0.1:0", "--auth-mode", "anonymous").ArtSigningKey)
}

// TestArtSigningKeyRefusesAShortKey reads the key through the command, the
// only seam cmd/ui has: internal/cli/ui's artSigningKey is unexported.
func TestArtSigningKeyRefusesAShortKey(t *testing.T) {
	useUnreachableKubeconfig(t)
	t.Setenv(cli.EnvArtSigningKey, "")
	o := captureUIOptions(t, "--bind-address", "127.0.0.1:0", "--auth-mode", "anonymous")
	require.Empty(t, o.ArtSigningKey, "no key configured: the ui signs per process")

	t.Setenv(cli.EnvArtSigningKey, "short")
	_, err := runUICommand(t, "--bind-address", "127.0.0.1:0", "--auth-mode", "anonymous")
	require.ErrorContains(t, err, cli.EnvArtSigningKey)
}

// --plex-guids reaches the Plex provider, on by default.
func TestUIPassesPlexGUIDs(t *testing.T) {
	useUnreachableKubeconfig(t)
	for _, tc := range []struct {
		argv []string
		want bool
	}{
		{[]string{"--bind-address", "127.0.0.1:0", "--auth-mode", "anonymous"}, true},
		{[]string{"--bind-address", "127.0.0.1:0", "--auth-mode", "anonymous", "--plex-guids=false"}, false},
	} {
		o := captureUIOptions(t, tc.argv...)
		require.NotNil(t, o.Plex, "%v", tc.argv)
		require.Equal(t, tc.want, o.Plex.PlexGUIDs, "%v", tc.argv)
	}
}

// TestUIWiresTheArtCacheFlag: --art-cache-bytes sizes /art's digest-keyed
// byte cache (W4.110; artwork design §B.8 as amended 2026-10-07), with
// ui.DefaultArtCacheBytes when unset and 0 turning the cache off.
func TestUIWiresTheArtCacheFlag(t *testing.T) {
	useUnreachableKubeconfig(t)
	for _, tc := range []struct {
		args []string
		want int64
	}{
		{nil, ui.DefaultArtCacheBytes},
		{[]string{"--art-cache-bytes=1048576"}, 1 << 20},
		{[]string{"--art-cache-bytes=0"}, 0},
	} {
		o := captureUIOptions(t, append([]string{"--bind-address", "127.0.0.1:0", "--auth-mode", "anonymous"}, tc.args...)...)
		require.Equal(t, tc.want, o.ArtCacheBytes, "%v", tc.args)
	}
}
