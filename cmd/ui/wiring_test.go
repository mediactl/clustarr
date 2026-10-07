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

	"github.com/stretchr/testify/require"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	captionapp "github.com/mediactl/clustarr/app/caption"
	catalogapp "github.com/mediactl/clustarr/app/catalog"
	grabapp "github.com/mediactl/clustarr/app/grab"
	importapp "github.com/mediactl/clustarr/app/import"
	indexapp "github.com/mediactl/clustarr/app/indexer"
	transcodeapp "github.com/mediactl/clustarr/app/transcode"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
)

// TestBothUICommandsWireEveryUIOption is the behavioural half of
// ui_projection_wiring_test.go. That test reads services.go and all.go as
// text, which is cheap and names the gap precisely, but a text match passes
// for a field that is named and never reaches runUI -- F-6 found exactly that
// shape. This one EXECUTES `clustarr ui` and `clustarr all`, with every
// service entrypoint stubbed, and inspects the ui.Options each one actually
// hands to runUI.
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
// The kubeconfig names an apiserver nothing listens on
// (squasharr_worker_test.go's unreachableKubeconfig): ctrl.GetConfig only
// parses it, and building ui's cache, projection and action writer dials
// nothing, so each command reaches runUI exactly as it would against a real
// cluster. With no kubeconfig at all, a nil Reader and a nil Actions are the
// correct result rather than a wiring gap, which is why this test supplies
// one.
func TestBothUICommandsWireEveryUIOption(t *testing.T) {
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(kubeconfig, []byte(unreachableKubeconfig), 0o600))
	t.Setenv("KUBECONFIG", kubeconfig)

	for _, argv := range [][]string{
		{"ui", "--bind-address", "127.0.0.1:0", "--auth-mode", "anonymous"},
		{"all", "--ui-auth-mode", "anonymous"},
	} {
		t.Run("clustarr "+argv[0], func(t *testing.T) {
			o := captureUIOptions(t, argv...)
			requireEveryUIOptionWired(t, "clustarr "+argv[0], o)
		})
	}
}

// captureUIOptions executes argv with every service entrypoint stubbed and
// returns the ui.Options runUI was called with. The command's context is
// cancelled when the test ends, which stops the cache and projection
// goroutines the command started.
func captureUIOptions(t *testing.T, argv ...string) ui.Options {
	t.Helper()

	catalog, index, grab, transcode, caption, importa, uiRun :=
		runCatalog, runIndex, runGrab, runTranscode, runCaption, runImport, runUI
	t.Cleanup(func() {
		runCatalog, runIndex, runGrab, runTranscode, runCaption, runImport, runUI =
			catalog, index, grab, transcode, caption, importa, uiRun
	})

	var (
		mu     sync.Mutex
		got    ui.Options
		called bool
	)
	runCatalog = func(context.Context, catalogapp.Options) error { return nil }
	runIndex = func(context.Context, indexapp.Options) error { return nil }
	runGrab = func(context.Context, grabapp.Options) error { return nil }
	runTranscode = func(context.Context, transcodeapp.Options) error { return nil }
	runCaption = func(context.Context, captionapp.Options) error { return nil }
	runImport = func(context.Context, importapp.Options) error { return nil }
	runUI = func(_ context.Context, o ui.Options) error {
		mu.Lock()
		defer mu.Unlock()
		got, called = o, true
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	root := NewRootCommand()
	root.SetArgs(argv)
	require.NoError(t, root.ExecuteContext(ctx), "clustarr %s", strings.Join(argv, " "))

	mu.Lock()
	defer mu.Unlock()
	require.True(t, called, "clustarr %s never called runUI", strings.Join(argv, " "))
	return got
}

// requireEveryUIOptionWired is TestBothUICommandsWireEveryUIOption's
// assertion, shared by both commands.
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
				"so nothing else in the tree goes red. Wire it in both services.go and all.go.",
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

// Both ui commands sign Plex's photo URLs with the key the installer puts in
// $CLUSTARR_ART_SIGNING_KEY, so the URLs Plex stores outlive a restart.
func TestBothUICommandsReadTheArtSigningKey(t *testing.T) {
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(kubeconfig, []byte(unreachableKubeconfig), 0o600))
	t.Setenv("KUBECONFIG", kubeconfig)
	key := strings.Repeat("0123456789abcdef", 4)
	t.Setenv(artSigningKeyEnv, key)

	for _, argv := range [][]string{
		{"ui", "--bind-address", "127.0.0.1:0", "--auth-mode", "anonymous"},
		{"all", "--ui-auth-mode", "anonymous"},
	} {
		t.Run("clustarr "+argv[0], func(t *testing.T) {
			require.Equal(t, []byte(key), captureUIOptions(t, argv...).ArtSigningKey)
		})
	}
}

func TestArtSigningKeyRefusesAShortKey(t *testing.T) {
	t.Setenv(artSigningKeyEnv, "")
	key, err := artSigningKey()
	require.NoError(t, err)
	require.Empty(t, key, "no key configured: the ui signs per process")

	t.Setenv(artSigningKeyEnv, "short")
	_, err = artSigningKey()
	require.ErrorContains(t, err, artSigningKeyEnv)
}

// --plex-guids reaches the Plex provider from both commands, on by
// default.
func TestBothUICommandsPassPlexGUIDs(t *testing.T) {
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(kubeconfig, []byte(unreachableKubeconfig), 0o600))
	t.Setenv("KUBECONFIG", kubeconfig)
	for _, tc := range []struct {
		argv []string
		want bool
	}{
		{[]string{"ui", "--bind-address", "127.0.0.1:0", "--auth-mode", "anonymous"}, true},
		{[]string{"ui", "--bind-address", "127.0.0.1:0", "--auth-mode", "anonymous", "--plex-guids=false"}, false},
		{[]string{"all", "--ui-auth-mode", "anonymous"}, true},
		{[]string{"all", "--ui-auth-mode", "anonymous", "--plex-guids=false"}, false},
	} {
		o := captureUIOptions(t, tc.argv...)
		require.NotNil(t, o.Plex, "%v", tc.argv)
		require.Equal(t, tc.want, o.Plex.PlexGUIDs, "%v", tc.argv)
	}
}
