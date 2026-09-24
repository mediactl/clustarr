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

// Command clustarr is the single binary behind every Clustarr service.
//
// §2 gives the project one image and one entrypoint: `clustarr <service>
// --role <role>`, plus `clustarr all` for kind and development. Five services
// in one binary keeps the images, the version stamp and the flag vocabulary
// identical everywhere, which is what makes a Deployment and a batch Job
// differ only in their arguments.
package main

import (
	"fmt"
	"os"

	ctrl "sigs.k8s.io/controller-runtime"
)

func main() {
	// SetupSignalHandler cancels the context on SIGTERM and SIGINT, and
	// kills the process on a second signal. It may only be called once per
	// process, so it is called here and threaded through cobra's context
	// rather than from each service.
	ctx := ctrl.SetupSignalHandler()

	if err := NewRootCommand().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(exitCode(err))
	}
}

// exitCode is the process exit status for an error from any subcommand: 1.
// The transcode worker, whose exit codes are a contract with its pool Job,
// is its own binary (cmd/squasharr-worker) and does not come through here.
func exitCode(error) int { return 1 }
