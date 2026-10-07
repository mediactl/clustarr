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

package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
)

// Main runs newCmd() under a signal context. controller-runtime's
// pkg/manager/signals is stdlib-only; its root re-export would link the
// manager into cmd/ui. SetupSignalHandler may be called once per process,
// which is why it is called here and threaded through cobra's context. A
// second signal exits 1 (the signals package), as does any error from RunE.
func Main(newCmd func() *cobra.Command) {
	ctx := signals.SetupSignalHandler()
	if err := newCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
