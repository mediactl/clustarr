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

package torrent

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// anacrolix's own lines go under grab.torrent.anacrolix, so an override for
// that name quiets them alone (design §4); the client's own lines keep the
// caller's logger, and a Config without one logs nowhere rather than through
// slog.Default.
func TestAnacrolixLogsUnderItsSubsystem(t *testing.T) {
	var buf bytes.Buffer
	root := logging.New(logging.Options{Output: &buf, Subsystems: map[string]slog.Level{"grab.torrent.anacrolix": slog.LevelError}})
	own := logging.SubsystemLogger(root, "grab.torrent")
	al := anacrolixLogger(Config{Logger: own})
	require.Equal(t, "grab.torrent.anacrolix", al.Handler().(*logging.Handler).Subsystem())
	al.Warn("announce failed")
	require.Empty(t, buf.String(), "the override drops anacrolix's WARN")
	al.Error("storage failed")
	require.Contains(t, buf.String(), `"subsystem":"grab.torrent.anacrolix"`)
	require.Same(t, own, clientLogger(Config{Logger: own}))
	require.False(t, clientLogger(Config{}).Enabled(context.Background(), logging.LevelFatal))
}
