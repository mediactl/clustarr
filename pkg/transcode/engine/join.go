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
	return Run(ctx, plan, path, output, Options{InputFormat: "concat", InputOptions: map[string]string{"safe": "0"}})
}
