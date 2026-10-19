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

package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/obinnaokechukwu/ffgo"
)

// Join remuxes parts -- one encode released in pieces, in order -- into
// output, every stream copied (the first part's CopyPlan, written in
// output's container): FFmpeg's concat demuxer reads them end to end as one
// input, and the DTS guard holds decode time moving forward across each
// seam. Import joins a movie released as Part 1 and Part 2 this way, since
// a movie is one file (28 Up's, 42 Up's and 49 Up's Blu-ray encodes,
// 2026-10-16, each imported as half the film).
func Join(ctx context.Context, parts []string, output string) (Result, error) {
	if len(parts) < 2 {
		return Result{}, fmt.Errorf("engine: a join takes two parts or more, not %d", len(parts))
	}
	c, ok := graftContainer(output)
	if !ok {
		return Result{}, fmt.Errorf("engine: join into %s: neither Matroska nor MP4", output)
	}
	plan, err := CopyPlan(parts[0])
	if err != nil {
		return Result{}, err
	}
	plan.Container = c
	dir, err := os.MkdirTemp("", "clustarr-join-")
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	var list strings.Builder
	list.WriteString("ffconcat version 1.0\n")
	for _, p := range parts {
		abs, err := filepath.Abs(p)
		if err != nil {
			return Result{}, err
		}
		list.WriteString("file '" + strings.ReplaceAll(abs, "'", `'\''`) + "'\n")
	}
	path := filepath.Join(dir, "parts.ffconcat")
	if err := os.WriteFile(path, []byte(list.String()), 0o600); err != nil {
		return Result{}, err
	}
	want, err := partsMillis(parts)
	if err != nil {
		return Result{}, err
	}
	res, err := Run(ctx, plan, path, output, Options{InputFormat: "concat", InputOptions: map[string]string{"safe": "0"}})
	if err != nil {
		return res, err
	}
	// FFmpeg's concat demuxer ends quietly at a part it cannot open or
	// read to its end, so a join that stopped short reads as done: the
	// output must run the parts' length end to end (2026-10-17, 49 Up
	// placed as its first part alone).
	if short := want - res.DurationMillis; short > joinToleranceMillis(want) {
		return res, fmt.Errorf("engine: the join of %d parts ran %d ms of their %d: a part could not be read to its end",
			len(parts), res.DurationMillis, want)
	}
	return res, nil
}

// partsMillis is the parts' length end to end, each as its container says;
// a part that cannot be opened fails the join before it starts.
func partsMillis(parts []string) (int64, error) {
	var total int64
	for _, p := range parts {
		d, err := ffgo.NewDecoder(p)
		if err != nil {
			return 0, fmt.Errorf("engine: open part %s: %w", filepath.Base(p), err)
		}
		total += d.Duration().Milliseconds()
		_ = d.Close()
	}
	return total, nil
}

// joinToleranceMillis is how much shorter than its parts a join may run:
// 2% and a second, for a container duration a subtitle outlasts. A part
// missing is minutes.
func joinToleranceMillis(want int64) int64 { return want/50 + 1000 }
