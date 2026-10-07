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

// Package uicli is cmd/ui's command tree (spec §3.6). It links no app/
// package, no pkg/k8s and no controller-runtime manager (§4.5.2). The
// package is named uicli so it never shadows ui.
package uicli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/mediactl/clustarr/internal/cli"
	"github.com/mediactl/clustarr/pkg/busconn"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

// RunFunc serves the ui with built options. NewCommandWith takes one so
// cmd/ui's tests can record the options a command line produced without
// serving anything.
type RunFunc func(context.Context, ui.Options) error

// NewCommand is `ui`, running ui.Run.
func NewCommand() *cobra.Command { return NewCommandWith(ui.Run) }

// buildPlexOptions builds ui.Options.Plex from --plex-provider,
// --external-url and --plex-guids. nil (feature off) exactly when
// --plex-provider is false; otherwise non-nil regardless of whether
// externalURL is set, since an empty one is a legal, if currently unusable,
// value (design spec §D.1: the roots answer 503 for it rather than the
// flag's absence silently turning the whole feature off) -- and
// ui.NewServer, not this func, is what logs the "no --external-url" warning
// once at startup.
func buildPlexOptions(enabled bool, externalURL string, plexGUIDs bool) *ui.PlexOptions {
	if !enabled {
		return nil
	}
	return &ui.PlexOptions{ExternalURL: externalURL, PlexGUIDs: plexGUIDs}
}

// NewCommandWith is `ui` with run in place of ui.Run.
func NewCommandWith(run RunFunc) *cobra.Command {
	root, lo, to := cli.NewRoot("ui", "Server-rendered web UI",
		"ui is the server-rendered web UI (templ + htmx + SSE): it never writes status and\n"+
			"owns no CRD of its own. Its authentication mode is chosen explicitly with\n"+
			"--auth-mode and it refuses to serve without one; the only mode, anonymous,\n"+
			"serves every request without a login and must sit behind ingress\n"+
			"authentication (design amendment §A3.5).")
	root.Args = cobra.NoArgs
	// The ui registers no Prometheus collectors and serves no /metrics
	// (§3.2): its process setup is the umask alone.
	root.PersistentPreRunE = cli.ApplyUmask

	var bindAddress string
	var namespace string
	var authMode string
	var natsURL string
	var plexProvider bool
	var plexGUIDs bool
	var externalURL string
	var pipelineHistory int
	var artCacheBytes int64

	fs := root.Flags()
	fs.StringVar(&bindAddress, "bind-address", ui.DefaultBindAddress,
		"Address the HTTP server listens on. Serves /healthz, /readyz, the Pipeline page and "+
			"its SSE stream on this one address -- ui runs no separate metrics or health port.")
	fs.StringVar(&authMode, "auth-mode", "",
		"Authentication mode, chosen explicitly: ui refuses to serve without one. The only mode is "+
			"anonymous, which serves every request without a login and must sit behind ingress "+
			"authentication (design amendment §A3.5).")
	fs.StringVar(&natsURL, "nats-url", cli.EnvOr(cli.EnvNATSURL, busconn.DefaultNATSURL),
		"JetStream endpoint ui reads artwork from (GET /art). Defaults to $"+cli.EnvNATSURL+". ui never "+
			"writes to it, so an unreachable endpoint degrades every page to placeholder art rather "+
			"than failing the process.")
	fs.BoolVar(&plexProvider, "plex-provider", true,
		"Serve the Plex Custom Metadata Provider at /plex/movies and /plex/tv (design spec §D). "+
			"Unauthenticated by protocol: it must not sit behind a public ingress.")
	fs.BoolVar(&plexGUIDs, "plex-guids", true,
		"Answer the movies, shows, seasons and episodes Plex knows with their plex:// GUIDs instead of "+
			"clustarr's own, so Plex Web offers Watchlist; ids come from a plex MetadataProvider. False "+
			"goes back to clustarr's GUIDs for new matches.")
	fs.StringVar(&namespace, "namespace", cli.EnvOr(cli.EnvNamespace, ""),
		"Namespace the Settings page creates namespaced objects in by default. Defaults to $"+cli.EnvNamespace+".")
	fs.StringVar(&externalURL, "external-url", cli.EnvOr(cli.EnvExternalURL, ""),
		"Absolute base every thumb, art and Image[].url the Plex provider hands Plex is built on, "+
			"e.g. https://clustarr.example.com. Defaults to $"+cli.EnvExternalURL+". Required for "+
			"--plex-provider to serve anything but 503.")
	fs.IntVar(&pipelineHistory, "pipeline-history", projection.DefaultPipelineHistory,
		"How many results the pipeline page keeps beside its in-flight entries, newest first "+
			"(two pages at the default page size); 0 shows in-flight entries only.")
	fs.Int64Var(&artCacheBytes, "art-cache-bytes", ui.DefaultArtCacheBytes,
		"Bytes of artwork the ui keeps in memory, by digest, to serve GET /art without a NATS read; "+
			"0 disables the cache (artwork design §B.8 as amended 2026-10-07).")

	root.RunE = func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		reader, waitForSync, acts := buildCluster(ctx)
		proj := buildProjection(ctx, reader, pipelineHistory)
		signingKey, err := artSigningKey()
		if err != nil {
			return err
		}
		b := buildBus(ctx, natsURL)
		defer b.close()
		tracing := *to
		tracing.ServiceName = "ui"
		// Every cluster-derived field; cmd/ui's wiring test executes the
		// command and fails on any func, pointer or interface field of
		// ui.Options left nil.
		return run(ctx, ui.Options{
			BindAddress:          bindAddress,
			AuthMode:             ui.AuthMode(authMode),
			Reader:               reader,
			WaitForSync:          waitForSync,
			Projected:            proj.Projected,
			Actions:              acts,
			Namespace:            namespace,
			Artwork:              b.artwork,
			ArtCacheBytes:        artCacheBytes,
			MetadataSearch:       b.search,
			PlexExtended:         b.extended,
			PlexExtras:           b.extras,
			ArtSigningKey:        signingKey,
			Plex:                 buildPlexOptions(plexProvider, externalURL, plexGUIDs),
			Entries:              proj.Entries,
			Subscribe:            proj.Subscribe,
			SubscribeDownloads:   proj.SubscribeDownloads,
			Library:              proj.Library,
			SubscribeLibrary:     proj.SubscribeLibrary,
			Unmatched:            proj.Unmatched,
			SubscribeUnmatched:   proj.SubscribeUnmatched,
			ImportLists:          proj.ImportLists,
			SubscribeImportLists: proj.SubscribeImportLists,
			Logging:              *lo,
			Tracing:              tracing,
		})
	}
	return root
}
