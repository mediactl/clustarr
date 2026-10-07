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

package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	torrentagent "github.com/mediactl/clustarr/app/grab/agent/torrent"
	"github.com/mediactl/clustarr/internal/cli"
	"github.com/mediactl/clustarr/internal/cli/ctrlflags"
)

// RunFunc starts the agent with parsed options. NewCommandWith takes one so
// cmd/agent's tests can record the options a command line produced without
// starting anything.
type RunFunc func(context.Context, Options) error

// NewCommand is `agent`, running Run.
func NewCommand() *cobra.Command { return NewCommandWith(Run) }

// NewCommandWith is `agent` with run in place of Run.
func NewCommandWith(run RunFunc) *cobra.Command {
	root, lo, to := cli.NewRoot("agent",
		"One Clustarr worker domain per process",
		"agent runs exactly one worker domain: "+strings.Join(domainNames, ", ")+".\n\n"+
			"It never takes a lease and never creates the JetStream topology: it waits for the\n"+
			"manager to create it. The download engines are agents too, rendered by the manager's\n"+
			"DownloadClient controller with --domain torrent-engine or usenet-engine.")
	root.Args = cobra.NoArgs
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if err := cli.ApplyUmask(cmd, args); err != nil {
			return err
		}
		if err := ctrlflags.RegisterMetrics(); err != nil {
			return fmt.Errorf("metrics: %w", err)
		}
		return nil
	}

	fs := root.Flags()
	common := ctrlflags.BindAgent(fs)
	d := DefaultOptions()
	var selfCheck bool

	fs.StringVar(&d.Domain, "domain", "",
		"The one domain this process runs: "+strings.Join(domainNames, ", ")+". Required.")
	fs.DurationVar(&d.DrainTimeout, "drain-timeout", 0,
		"How long running handlers keep their context once shutdown begins. Defaults to the longest "+
			"declared AckWait among the domain's durables; --graceful-shutdown-timeout then defaults to it plus 15s.")
	fs.DurationVar(&d.TopologyWait, "topology-wait", d.TopologyWait,
		"How long to wait at start for the manager to have created every stream, bucket and durable.")
	fs.BoolVar(&selfCheck, "self-check", false,
		"Check what this build links (the torrent piece completion), print a JSON report and exit. "+
			"Needs no domain, no cluster and no NATS.")

	fs.StringVar(&d.DataDir, "data-dir", d.DataDir,
		"RWX media volume. Domains import, caption, torrent-engine and usenet-engine.")
	fs.Int64Var(&d.SampleMaxBytes, "sample-max-bytes", d.SampleMaxBytes,
		"Video size floor, in bytes: a video file smaller than this whose name does not mark it a "+
			"sample is a suspected sample, listed as unmatched by a rescan and rejected by a "+
			"completed-download import unless the import is manual. 0 disables the size rule. Domain import.")
	fs.StringVar(&d.TraktBaseURL, "trakt-base-url", cli.EnvOr(cli.EnvTraktBaseURL, ""),
		"Trakt API the import lists' syncs reach; the manager's device-code flow must name the same host. "+
			"Empty is https://api.trakt.tv. Defaults to $"+cli.EnvTraktBaseURL+". Domain import.")
	fs.StringVar(&d.PlexBaseURL, "plex-base-url", cli.EnvOr(cli.EnvPlexBaseURL, ""),
		"Plex Discover API the import lists' Plex watchlist syncs reach. Empty is Plex's own. "+
			"Defaults to $"+cli.EnvPlexBaseURL+". Domain import.")
	fs.StringVar(&d.IndexPath, "index-path", cli.EnvOr(cli.EnvIndexPath, d.IndexPath),
		"SQLite release index file, on the RWO volume. Defaults to $"+cli.EnvIndexPath+". Domain index.")
	fs.StringVar(&d.IndexDSN, "index-dsn", cli.EnvOr(cli.EnvIndexDSN, d.IndexDSN),
		"Postgres DSN for the release index. Non-empty selects Postgres and ignores --index-path. "+
			"Defaults to $"+cli.EnvIndexDSN+". Domain index.")
	fs.StringVar(&d.FacadeBindAddress, "facade-bind-address", cli.EnvOr(cli.EnvFacadeBindAddress, d.FacadeBindAddress),
		`Address the Torznab facade binds to. "0" disables it. Defaults to $`+cli.EnvFacadeBindAddress+". Domain index.")
	fs.StringVar(&d.FacadeAPIKeySecret, "facade-api-key-secret",
		cli.EnvOr(cli.EnvFacadeAPIKeySecret, d.FacadeAPIKeySecret),
		"Secret in --namespace whose every non-blank entry is an API key the Torznab facade accepts; "+
			"created with one random key when absent. The facade never serves without a key. "+
			"Defaults to $"+cli.EnvFacadeAPIKeySecret+". Domain index.")
	fs.StringVar(&d.DownloadClient, "download-client", d.DownloadClient,
		"The DownloadClient this torrent engine replica serves; its \"<client>-<ordinal>\" identity comes "+
			"from $POD_NAME (else the hostname), which must be \"<client>-engine-<N>\". Domain torrent-engine.")
	fs.StringVar(&d.Engine, "engine", d.Engine,
		"This engine's identity, \"<client>-<ordinal>\". Domains torrent-engine (or --download-client) and usenet-engine.")
	fs.StringVar(&d.ScratchDir, "scratch-dir", d.ScratchDir,
		"Engine working area: the usenet engine's yEnc assembly, PAR2 repair and extraction, or a torrent "+
			"engine's downloads before publish. Domains torrent-engine and usenet-engine.")
	fs.StringVar(&d.PublishDir, "publish-dir", d.PublishDir,
		"Where the usenet engine publishes finished content, as <dir>/<category>/<name>; "+
			"empty means --data-dir. Under --data-dir, on the same filesystem as --scratch-dir. Domain usenet-engine.")

	root.RunE = func(cmd *cobra.Command, _ []string) error {
		if selfCheck {
			return runSelfCheck(cmd.OutOrStdout())
		}
		o := d
		o.Options = *common
		o.Logging, o.Tracing = *lo, *to
		if _, ok := domains[o.Domain]; !ok {
			return errDomain
		}
		// A domain flag set for a domain that does not read it is a
		// mistake, not a no-op. An environment default is not "set".
		for _, f := range slices.Sorted(maps.Keys(domainFlags)) {
			if cmd.Flags().Changed(f) && !slices.Contains(domainFlags[f], o.Domain) {
				return fmt.Errorf("--%s is only meaningful with --domain %s", f, orList(domainFlags[f]))
			}
		}
		if o.Domain == "torrent-engine" {
			if (o.Engine == "") == (o.DownloadClient == "") {
				return errors.New("--domain torrent-engine needs exactly one of --engine or --download-client")
			}
			if o.DownloadClient != "" {
				pod, err := torrentagent.PodName(os.Getenv, os.Hostname)
				if err != nil {
					return err
				}
				if o.Engine, err = torrentagent.EngineIdentity(o.DownloadClient, pod); err != nil {
					return err
				}
			}
		}
		if !cmd.Flags().Changed("drain-timeout") {
			o.DrainTimeout = drainFor(o.Domain)
		}
		if !cmd.Flags().Changed("graceful-shutdown-timeout") && o.DrainTimeout > 0 {
			o.GracefulShutdownTimeout = o.DrainTimeout + 15*time.Second
		}
		if err := o.Validate(); err != nil {
			return err
		}
		return run(cmd.Context(), o)
	}
	return root
}
