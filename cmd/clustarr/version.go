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

package main

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/mediactl/clustarr/pkg/version"
)

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the clustarr version",
		Long: "Print the version and commit this binary was built from, " +
			"as `clustarr <version>+<commit>`, followed by the Go toolchain and platform.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "clustarr %s\n", version.String()); err != nil {
				return err
			}
			_, err := fmt.Fprintf(out, "go %s %s/%s\n",
				runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return err
		},
	}
}
