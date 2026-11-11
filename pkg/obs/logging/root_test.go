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

package logging

import (
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/version"
)

func firstRecord(t *testing.T, out string) map[string]any {
	t.Helper()
	line := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
	var rec map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &rec), out)
	return rec
}

func TestRootStampsTheService(t *testing.T) {
	t.Setenv("POD_NAME", "manager-7")
	buf := &syncBuffer{}
	ctx, root, err := Root(context.Background(), Options{Output: buf}, ServiceFor("manager"))
	require.NoError(t, err)
	require.Same(t, root, FromContext(ctx))
	root.Info("up")
	rec := firstRecord(t, buf.String())
	require.Equal(t, map[string]any{
		"namespace": "clustarr", "name": "manager", "version": version.Version, "instance.id": "manager-7",
	}, rec["service"])
	require.Equal(t, "INFO", rec["level"])
}

func TestRootDrainsGoLogAndSlogDefault(t *testing.T) {
	buf := &syncBuffer{}
	_, _, err := Root(context.Background(), Options{Output: buf}, ServiceFor("ui"))
	require.NoError(t, err)
	log.Print("from the log package")
	slog.Default().Info("from slog.Default")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2, buf.String())
	for _, l := range lines {
		require.Contains(t, l, `"subsystem":"stdlog"`)
		require.Contains(t, l, `"level":"INFO"`)
	}
}

func TestStdLoggerWritesAtItsLevel(t *testing.T) {
	buf := &syncBuffer{}
	ctx, _, err := Root(context.Background(), Options{Output: buf}, ServiceFor("ui"))
	require.NoError(t, err)
	StdLogger(ctx, "ui", slog.LevelWarn).Print("http: TLS handshake error")
	rec := firstRecord(t, buf.String())
	require.Equal(t, "WARN", rec["level"])
	require.Equal(t, "ui", rec["subsystem"])
	require.Equal(t, "http: TLS handshake error", rec["msg"])
}

func TestRootReadsTheFileAndWatchesIt(t *testing.T) {
	path := t.TempDir() + "/logging.yaml"
	writeLevel(t, path, "warn")
	buf := &syncBuffer{}
	ctx, root, err := Root(context.Background(), Options{Output: buf, ConfigPath: path}, ServiceFor("manager"))
	require.NoError(t, err)
	require.Equal(t, slog.LevelWarn, LevelsOf(root).Floor())
	require.NotNil(t, ctx)
}

func TestRootWithAMissingFileIsNotAnError(t *testing.T) {
	buf := &syncBuffer{}
	_, root, err := Root(context.Background(), Options{Output: buf, ConfigPath: t.TempDir() + "/none.yaml"}, ServiceFor("manager"))
	require.NoError(t, err)
	require.Equal(t, slog.LevelInfo, LevelsOf(root).Floor())
	require.Empty(t, buf.String())
}
