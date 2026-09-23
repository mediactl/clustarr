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
	"context"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/mediactl/clustarr/test/fixtures/seed"
	"github.com/mediactl/clustarr/test/fixtures/seeder"
)

func newSeederCommand() *cobra.Command {
	var (
		btAddr       string
		httpAddr     string
		announceHost string
		peerHost     string
		dataDir      string
		contentBytes int64
		contentPath  string
	)
	cmd := &cobra.Command{
		Use:   "seeder",
		Short: "Seed a known, deterministic torrent over --bt-addr and serve its .torrent and BEP3 tracker on --http-addr",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

			if announceHost == "" {
				logger.Warn("seeder: --announce-host is unset; the .torrent's tracker URL will use the " +
					"HTTP listener's own bound address, which is only reachable from another Pod if " +
					"--http-addr itself named a routable host. Set --announce-host to this Deployment's " +
					"Service DNS name in cluster.")
			}

			srv, err := seeder.New(seeder.Config{
				BTAddr:       btAddr,
				HTTPAddr:     httpAddr,
				AnnounceHost: announceHost,
				PeerHost:     peerHost,
				ContentBytes: contentBytes,
				ContentPath:  contentPath,
				DataDir:      dataDir,
				Logger:       logger,
			})
			if err != nil {
				return err
			}
			logger.Info("seeder: seeded and ready", "bt_addr", srv.BTAddr(), "http_addr", srv.HTTPAddr(),
				"info_hash", srv.InfoHash().HexString())

			return srv.Serve(context.Background())
		},
	}
	cmd.Flags().StringVar(&btAddr, "bt-addr", ":6889", "BitTorrent listen address")
	cmd.Flags().StringVar(&httpAddr, "http-addr", ":8080", "HTTP listen address for the tracker and .torrent file")
	cmd.Flags().StringVar(&announceHost, "announce-host", "",
		"host:port embedded in the served .torrent's announce URL (e.g. this Deployment's Service DNS name); "+
			"empty derives it from --http-addr's own bound address, which is only correct for a loopback address")
	cmd.Flags().StringVar(&peerHost, "peer-host", "",
		"IP other Pods dial to reach this seeder's BitTorrent listener (e.g. status.podIP via the Downward "+
			"API); empty uses --bt-addr's host if it named one, else the first non-loopback IPv4 address found")
	cmd.Flags().StringVar(&dataDir, "data-dir", "/data/.e2e-fixtures/seeder", "directory for the seeded content and torrent client state")
	cmd.Flags().Int64Var(&contentBytes, "content-bytes", seeder.DefaultContentBytes,
		"size of the synthesized content file; ignored when --content-path is set")
	cmd.Flags().StringVar(&contentPath, "content-path", seed.BakedClipPath,
		"real media file copied verbatim to become the torrent's content, so a completed transfer against "+
			"this seeder is real, ffprobe-able media rather than synthetic bytes (X12c, "+
			"docs/superpowers/plans/2026-09-23-gap-fixes.md); empty falls back to --content-bytes worth of "+
			"synthesized, non-media bytes, which is what this package's own tests use since they run "+
			"outside the fixture image and have no baked clip to point at")
	return cmd
}
