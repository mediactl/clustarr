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
	"github.com/mediactl/clustarr/importarr"
	"github.com/mediactl/clustarr/indexarr"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/squasharr"
	"github.com/mediactl/clustarr/ui"
)

// The service entrypoints, as variables so the package's tests can execute a
// command line and inspect the options it produced without starting a manager
// or reaching for a kubeconfig.
var (
	runCatalogarr = catalogarr.Run
	runImportarr  = importarr.Run
	runIndexarr   = indexarr.Run
	runGrabarr    = grabarr.Run
	runSquasharr  = squasharr.Run
	runCaptionarr = captionarr.Run
	runUI         = ui.Run
)

// tracingFor derives the per-service tracing.Options from the root command's
// shared flags, stamping ServiceName so every span this process starts
// carries a resource.service.name distinct from every other Clustarr
// process. common is dereferenced here, once per command execution, after
// cobra has finished parsing -- never stored, so two subcommands (or two
// `execute` calls in a test) never share a mutable tracing.Options.
func tracingFor(common *tracing.Options, serviceName string) tracing.Options {
	to := *common
	to.ServiceName = serviceName
	return to
}

// roleUsage renders a --role help string from a service's own role list, so
// the help can never drift from what Validate accepts.
func roleUsage[R fmt.Stringer](roles []R) string {
	names := make([]string, 0, len(roles))
	for _, r := range roles {
		names = append(names, r.String())
	}
	return "What this replica does: " + strings.Join(names, "|") + "."
}

func newCatalogarrCommand(lo *logging.Options, to *tracing.Options) *cobra.Command {
	defaults := catalogarr.DefaultOptions()
	var role string

	cmd := &cobra.Command{
		Use:   "catalogarr",
		Short: "Inventory, metadata and release decisions",
		Long: "catalogarr owns catalog.clustarr.io: inventory, the metadata gateway and\n" +
			"release decisions (search, grab, rss-matcher).\n\n" +
			"Everything entering the library -- root-folder rescan, import lists and\n" +
			"completed-download import -- belongs to `clustarr importarr` (amendment §A1.2,\n" +
			"§A1.3), not here.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
	}
	common := bindCommonFlags(cmd.Flags())
	cmd.Flags().StringVar(&role, "role", defaults.Role.String(),
		roleUsage(catalogarr.Roles())+
			" §3 puts the controllers, the queue workers and the history sink in one"+
			" Deployment and the metadata gateway in its own, so this one accepts a"+
			" comma-separated combination too.")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return runCatalogarr(cmd.Context(), catalogarr.Options{
			Options: *common,
			Role:    catalogarr.Role(role),
			Logging: *lo,
			Tracing: tracingFor(to, catalogarr.ServiceName),
		})
	}
	return cmd
}

func newIndexarrCommand(lo *logging.Options, to *tracing.Options) *cobra.Command {
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
	cmd.Flags().StringVar(&indexPath, "index-path", envOr(indexPathEnv, defaults.IndexPath),
		"SQLite release index file, on the RWO volume. Defaults to $"+indexPathEnv+".")
	cmd.Flags().StringVar(&facade, "facade-bind-address", defaults.FacadeBindAddress,
		`Address the Torznab facade binds to. "0" disables it.`)

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return runIndexarr(cmd.Context(), indexarr.Options{
			Options:           *common,
			Role:              indexarr.Role(role),
			IndexPath:         indexPath,
			FacadeBindAddress: facade,
			Logging:           *lo,
			Tracing:           tracingFor(to, indexarr.ServiceName),
		})
	}
	return cmd
}

func newGrabarrCommand(lo *logging.Options, to *tracing.Options) *cobra.Command {
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
			Logging:    *lo,
			Tracing:    tracingFor(to, grabarr.ServiceName),
		})
	}
	return cmd
}

func newSquasharrCommand(lo *logging.Options, to *tracing.Options) *cobra.Command {
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
			Logging: *lo,
			Tracing: tracingFor(to, squasharr.ServiceName),
		})
	}
	return cmd
}

func newCaptionarrCommand(lo *logging.Options, to *tracing.Options) *cobra.Command {
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
			Logging: *lo,
			Tracing: tracingFor(to, captionarr.ServiceName),
		})
	}
	return cmd
}

func newImportarrCommand(lo *logging.Options, to *tracing.Options) *cobra.Command {
	defaults := importarr.DefaultOptions()
	var (
		role     string
		dataPath string
	)

	cmd := &cobra.Command{
		Use:   "importarr",
		Short: "Root-folder rescan, import lists and completed-download import",
		Long: "importarr owns everything entering the library: root-folder rescan, import\n" +
			"lists and completed-download import. It owns no CRD group of its own; its\n" +
			"controllers and workers read and write catalog.clustarr.io and\n" +
			"download.clustarr.io resources under their own field manager (amendment §A1.6).",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
	}
	common := bindCommonFlags(cmd.Flags())
	cmd.Flags().StringVar(&role, "role", defaults.Role.String(), roleUsage(importarr.Roles()))
	cmd.Flags().StringVar(&dataPath, "data-path", defaults.DataPath,
		"RWX media volume, mounted by the importarr-worker Deployment. The controller "+
			"Deployment does not mount it and only needs this to be non-empty.")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return runImportarr(cmd.Context(), importarr.Options{
			Options:  *common,
			Role:     importarr.Role(role),
			DataPath: dataPath,
			Logging:  *lo,
			Tracing:  tracingFor(to, importarr.ServiceName),
		})
	}
	return cmd
}

func newUICommand(lo *logging.Options, to *tracing.Options) *cobra.Command {
	var bindAddress string

	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Server-rendered web UI",
		Long: "ui is the server-rendered web UI (templ + htmx + SSE): it never writes status and\n" +
			"owns no CRD of its own, so it takes no --role. It ships with no login of its own\n" +
			"and must sit behind ingress authentication (design amendment §A3.5).",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
	}
	cmd.Flags().StringVar(&bindAddress, "bind-address", ui.DefaultBindAddress,
		"Address the HTTP server listens on. Serves /healthz, the Pipeline page and its "+
			"SSE stream on this one address -- ui runs no separate metrics or health port.")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		return runUI(cmd.Context(), ui.Options{
			BindAddress: bindAddress,
			// TODO(M3): back Entries with a controller-runtime cache-backed
			// projection over the Pipeline resources. Until then the Pipeline
			// page renders with no rows rather than reaching for a cluster ui
			// has no client for.
			Logging: *lo,
			Tracing: tracingFor(to, "ui"),
		})
	}
	return cmd
}
