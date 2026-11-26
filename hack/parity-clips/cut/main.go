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

// Command cut is hack/parity-clips.sh's stream copy: it reads a Matroska
// file from standard input and writes the first -t of it to the named file
// with every stream copied (ffmpeg -i pipe:0 -map 0 -c copy -t 60 out), on
// ffgo through internal/testmedia, so no script runs the ffmpeg CLI
// (TestNoScriptRunsFFmpeg). It needs FFmpeg 9's libraries as the tests do:
// `make native-assets` and the Makefile's NATIVE_ENV.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mediactl/clustarr/internal/testmedia"
)

func main() {
	until := flag.Duration("t", time.Minute, "how much of the input to keep")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: cut [-t 1m] OUT < IN")
		os.Exit(2)
	}
	if err := testmedia.RemuxFile("pipe:0", flag.Arg(0), testmedia.Retag{Until: *until}); err != nil {
		fmt.Fprintln(os.Stderr, "cut:", err)
		os.Exit(1)
	}
}
