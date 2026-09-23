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
	"errors"
	"fmt"
	"os"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/squasharr"
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

// exitCode is the process exit status for an error from any subcommand: 1,
// except for a squasharr transcode worker, whose code is the contract with
// its Job's podFailurePolicy (Phase E ruling R4) and must reach the kubelet
// unchanged. Exit 3 (invalid source) and 4 (verification failed) fail the
// Job outright; a 1 in their place would be retried backoffLimit times,
// re-transcoding a bad source for the same answer.
//
// It matches the concrete *squasharr.ExitError rather than any error with an
// ExitCode method: *exec.ExitError has one too, and an ffmpeg exit status
// wrapped somewhere in another service's error must not become clustarr's.
func exitCode(err error) int {
	var ee *squasharr.ExitError
	if errors.As(err, &ee) && ee.Code > 0 {
		return ee.Code
	}
	return 1
}
