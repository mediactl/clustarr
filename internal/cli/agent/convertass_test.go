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

package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles/asssrt"
)

// `agent convert-ass-sidecars` replaces a library's .ass sidecars with
// .srt ones once (standard.Version 5's runbook step) and prints its report
// as JSON; it needs no domain, no cluster and no NATS.
func TestConvertASSSidecarsCommand(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "movies", "Film (2020)")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Film (2020).mp4"), []byte("v"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Film (2020).en.ass"), []byte("[Script Info]\n\n[Events]\n"+
		"Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n"+
		"Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Hello\n"), 0o644))
	backup := filepath.Join(root, ".clustarr", "ass-backup")

	cmd := NewCommandWith(nil)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"convert-ass-sidecars", "--root", root, "--backup-dir", backup})
	require.NoError(t, cmd.Execute())
	var rep asssrt.Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &rep), out.String())
	require.Equal(t, 1, rep.Converted)
	_, err := os.Stat(filepath.Join(dir, "Film (2020).en.srt"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(backup, "movies", "Film (2020)", "Film (2020).en.ass"))
	require.NoError(t, err)
}
