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
	"strings"

	"github.com/spf13/cobra"

	"github.com/mediactl/clustarr/captionarr"
	"github.com/mediactl/clustarr/catalogarr"
	"github.com/mediactl/clustarr/grabarr"
	"github.com/mediactl/clustarr/indexarr"
	"github.com/mediactl/clustarr/squasharr"
)

// The service entrypoints, as variables so the package's tests can execute a
// command line and inspect the options it produced without starting a manager
// or reaching for a kubeconfig.
var (
	runCatalogarr = catalogarr.Run
	runIndexarr   = indexarr.Run
	runGrabarr    = grabarr.Run
	runSquasharr  = squasharr.Run
	runCaptionarr = captionarr.Run
)

// roleUsage renders a --role help string from a service's own role list, so
// the help can never drift from what Validate accepts.
func roleUsage[R fmt.Stringer](roles []R) string {
	names := make([]string, 0, len(roles))
	for _, r := range roles {
		names = append(names, r.String())
	}
	return "What this replica does: " + strings.Join(names, "|") + "."
}

func newCatalogarrCommand() *cobra.Command {
	defaults := catalogarr.DefaultOptions()
	var role string

	cmd := &cobra.Command{
		Use:   "catalogarr",
		Short: "Inventory, metadata, import lists, decisions and import",
		Long: "catalogarr owns catalog.clustarr.io: the media items themselves, their metadata,\n" +
			"import lists, quality decisions and the importer that turns a finished Download\n" +
			"into a MediaFile.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
	}
	common := bindCommonFlags(cmd.Flags())
	cmd.Flags().StringVar(&role, "role", defaults.Role.String(), roleUsage(catalogarr.Roles()))

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return runCatalogarr(cmd.Context(), catalogarr.Options{
			Options: *common,
			Role:    catalogarr.Role(role),
		})
	}
	return cmd
}

func newIndexarrCommand() *cobra.Command {
	defaults := indexarr.DefaultOptions()
	var (
		role      string
		indexPath string
		facade    string
	)

	cmd := &cobra.Command{
		Use:   "indexarr",
		Short: "Aggregate remote indexers into one search engine",
		Long: "indexarr owns index.clustarr.io: indexer definitions, the search RPC, the RSS\n" +
			"worker, the SQLite release index and the Torznab facade.\n\n" +
			"It runs as exactly one replica with a Recreate strategy: the release index is a\n" +
			"SQLite database on an RWO volume, and a second writer would corrupt it.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
	}
	common := bindCommonFlags(cmd.Flags())
	cmd.Flags().StringVar(&role, "role", defaults.Role.String(), roleUsage(indexarr.Roles()))
	cmd.Flags().StringVar(&indexPath, "index-path", defaults.IndexPath,
		"SQLite release index file, on the RWO volume.")
	cmd.Flags().StringVar(&facade, "facade-bind-address", defaults.FacadeBindAddress,
		`Address the Torznab facade binds to. "0" disables it.`)

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return runIndexarr(cmd.Context(), indexarr.Options{
			Options:           *common,
			Role:              indexarr.Role(role),
			IndexPath:         indexPath,
			FacadeBindAddress: facade,
		})
	}
	return cmd
}

func newGrabarrCommand() *cobra.Command {
	defaults := grabarr.DefaultOptions()
	var (
		role    string
		engine  string
		dataDir string
		scratch string
	)

	cmd := &cobra.Command{
		Use:   "grabarr",
		Short: "Download clients and the torrent and usenet engines",
		Long: "grabarr owns download.clustarr.io: the DownloadClient and Download controllers,\n" +
			"and the embedded engines that do the transfers. An engine pod runs the same\n" +
			"binary with --role torrent-engine or --role usenet-engine and an --engine\n" +
			"identity, and watches only the Downloads labelled for it.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
	}
	common := bindCommonFlags(cmd.Flags())
	cmd.Flags().StringVar(&role, "role", defaults.Role.String(), roleUsage(grabarr.Roles()))
	cmd.Flags().StringVar(&engine, "engine", defaults.Engine,
		"This engine's identity, \"<client>-<ordinal>\". Required for an engine role.")
	cmd.Flags().StringVar(&dataDir, "data-dir", defaults.DataDir,
		"RWX media volume.")
	cmd.Flags().StringVar(&scratch, "scratch-dir", defaults.ScratchDir,
		"Usenet engine working area for yEnc assembly, PAR2 repair and extraction.")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return runGrabarr(cmd.Context(), grabarr.Options{
			Options:    *common,
			Role:       grabarr.Role(role),
			Engine:     engine,
			DataDir:    dataDir,
			ScratchDir: scratch,
		})
	}
	return cmd
}

func newSquasharrCommand() *cobra.Command {
	defaults := squasharr.DefaultOptions()
	var (
		role    string
		slots   string
		dataDir string
		jobName string
	)

	cmd := &cobra.Command{
		Use:   "squasharr",
		Short: "Distributed HEVC 10-bit transcoding",
		Long: "squasharr owns transcode.clustarr.io: it watches MediaFiles for non-compliant\n" +
			"video and schedules HEVC 10-bit / AAC transcodes as batch Jobs, admitted against\n" +
			"a per-hardware slot budget. The Job's own entrypoint is --role worker.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
	}
	common := bindCommonFlags(cmd.Flags())
	cmd.Flags().StringVar(&role, "role", defaults.Role.String(), roleUsage(squasharr.Roles()))
	cmd.Flags().StringVar(&slots, "slots", squasharr.FormatSlots(defaults.Slots),
		"Concurrent transcode budget per hardware class, e.g. cpu=2,nvidia=1,intel=1. "+
			"A budget of 0 means that class is never admitted.")
	cmd.Flags().StringVar(&dataDir, "data-dir", defaults.DataDir,
		"RWX media volume.")
	cmd.Flags().StringVar(&jobName, "job", defaults.JobName,
		"TranscodeJob this worker is transcoding. Required for --role worker.")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		budget, err := squasharr.ParseSlots(slots)
		if err != nil {
			return err
		}
		return runSquasharr(cmd.Context(), squasharr.Options{
			Options: *common,
			Role:    squasharr.Role(role),
			Slots:   budget,
			DataDir: dataDir,
			JobName: jobName,
		})
	}
	return cmd
}

func newCaptionarrCommand() *cobra.Command {
	defaults := captionarr.DefaultOptions()
	var (
		role    string
		dataDir string
	)

	cmd := &cobra.Command{
		Use:   "captionarr",
		Short: "Distributed subtitle planning and fetching",
		Long: "captionarr owns subtitle.clustarr.io: it plans which subtitles a MediaFile still\n" +
			"wants and fetches them. The fetch workers share a key/value token bucket, so any\n" +
			"number of them stays inside a provider's rate limit.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
	}
	common := bindCommonFlags(cmd.Flags())
	cmd.Flags().StringVar(&role, "role", defaults.Role.String(), roleUsage(captionarr.Roles()))
	cmd.Flags().StringVar(&dataDir, "data-dir", defaults.DataDir,
		"RWX media volume; sidecars are written next to their video file.")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return runCaptionarr(cmd.Context(), captionarr.Options{
			Options: *common,
			Role:    captionarr.Role(role),
			DataDir: dataDir,
		})
	}
	return cmd
}
