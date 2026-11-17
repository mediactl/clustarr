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

package ytdlp

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"time"
)

// outputCap bounds what is kept of each of the program's streams.
const outputCap = 64 << 10

// run runs the yt-dlp program at path with args, in dir, and returns what
// it wrote. It is clustarr's only start of a program that is not itself
// (test/guards.TestNoProductionCodeStartsAProcess): path is the agent's
// --ytdlp-path, set by the chart to the one program the trailers image
// holds, and args is built by [args] from a page clustarr composed.
//
// The program gets no environment of the agent's but a PATH and a HOME it
// can write to: no NATS URL, no pod identity. When ctx ends it is killed.
func run(ctx context.Context, path, dir string, args []string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + os.TempDir(), "LANG=C.UTF-8"}
	cmd.WaitDelay = 10 * time.Second
	var out, errb capped
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return out.String(), errb.String(), err
}

// capped keeps the last outputCap bytes written to it.
type capped struct{ bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	n := len(p)
	c.Buffer.Write(p)
	if over := c.Len() - outputCap; over > 0 {
		c.Next(over)
	}
	return n, nil
}
