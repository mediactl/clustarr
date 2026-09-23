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

import "github.com/spf13/cobra"

// NewRootCommand wires one subcommand per fixture Deployment: seeder and
// nntp-stub (Phase D), opensubtitles-stub and gestdown-stub (Phase F,
// F-7). Each subcommand owns its own flags and never imports another
// fixture's package.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "clustarr-e2e-fixtures",
		Short:         "In-cluster stub services for the kind-based e2e suite",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newTMDBStubCommand())
	root.AddCommand(newTVDBStubCommand())
	root.AddCommand(newTorznabStubCommand())
	root.AddCommand(newSeedCommand())
	root.AddCommand(newSeederCommand())
	root.AddCommand(newNNTPStubCommand())
	root.AddCommand(newOpenSubtitlesStubCommand())
	root.AddCommand(newGestdownStubCommand())
	return root
}
