/*
Copyright (C) 2026 The Clustarr Authors

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

package native

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/internal/testmedia"
	"github.com/mediactl/clustarr/pkg/ffruntime"
)

// lockedBuffer is a log sink two reads write to at once.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return bytes.Clone(l.b.Bytes())
}

// FFmpeg's complaint about a file's bytes says which file: on talos,
// 2026-11-27, two import pods read eleven damaged episodes of one release
// end to end and logged 116 lines like "0x00 at pos 618735295 (0x24e126bf)
// invalid as first byte of an EBML number", none naming a file (owner: "Do
// all 3"). Two files with their holes in different places are read at the
// same time here, and each complaint about a position names the file whose
// hole holds it.
func TestFFmpegsComplaintsNameTheFileTheyAreAbout(t *testing.T) {
	p := testProber(t)
	whole := testmedia.Make(t, "clip.mkv", testmedia.Spec{
		Duration: 60 * time.Second,
		Video: []testmedia.Video{{
			Source: "testsrc2=size=320x180:rate=24", Options: map[string]string{"preset": "ultrafast", "g": "24"},
		}},
		Audio: []testmedia.Audio{{Source: "sine=frequency=440"}},
	})
	st, err := os.Stat(whole)
	require.NoError(t, err)
	size := float64(st.Size())
	early, late := zeroed(t, whole, [2]float64{0.2, 0.3}), zeroed(t, whole, [2]float64{0.6, 0.7})
	// A complaint's position lies in its file's hole or just past it, never
	// in the other's.
	holes := map[string][2]float64{early: {0.2 * size, 0.45 * size}, late: {0.6 * size, 0.85 * size}}

	var buf lockedBuffer
	ffruntime.RouteLog(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { ffruntime.RouteLog(nil) })

	var wg sync.WaitGroup
	for _, path := range []string{early, late, early, late} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := p.Health(context.Background(), path)
			assert.NoError(t, err)
			assert.True(t, h.Damaged(), "%s: %+v", path, h)
		}()
	}
	wg.Wait()

	at := regexp.MustCompile(`at pos (\d+) `)
	seen := map[string]int{}
	sc := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	for sc.Scan() {
		var rec struct{ Msg, Message, File string }
		require.NoError(t, json.Unmarshal(sc.Bytes(), &rec))
		if rec.Msg != "ffmpeg" {
			continue
		}
		hole, known := holes[rec.File]
		require.True(t, known, "an FFmpeg line with no file, or another's: %s", sc.Text())
		seen[rec.File]++
		if m := at.FindStringSubmatch(rec.Message); m != nil {
			pos, err := strconv.ParseFloat(m[1], 64)
			require.NoError(t, err)
			assert.True(t, pos >= hole[0] && pos <= hole[1], "%q is about a position outside %s's hole", rec.Message, rec.File)
		}
	}
	assert.Positive(t, seen[early], "the file with the early hole drew no complaint")
	assert.Positive(t, seen[late], "the file with the late hole drew no complaint")
}
