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
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/mediactl/clustarr/test/fixtures/seed"
	"github.com/mediactl/clustarr/test/fixtures/seeder"
	"github.com/mediactl/clustarr/test/fixtures/torznabstub"
)

func newTorznabStubCommand() *cobra.Command {
	var (
		addr           string
		logPath        string
		payloads       torznabstub.Payloads
		torrentContent string
	)
	cmd := &cobra.Command{
		Use:   "torznab-stub",
		Short: "Serve the fixture Torznab indexer on --addr",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
			// The seeder seeds the baked clip; its info hash is a pure
			// function of those bytes (seeder.InfoHash), so the feed can
			// carry it without asking the seeder. Computed before the
			// listener opens, so no answer ever lacks it.
			if payloads.TorrentURL != "" && torrentContent != "" {
				h, err := seeder.InfoHash(torrentContent)
				if err != nil {
					return fmt.Errorf("torznab-stub: the seeder's info hash: %w", err)
				}
				payloads.TorrentInfoHash = h.HexString()
			}
			logger.Info("torznab-stub: listening", "addr", addr, "request_log", logPath,
				"torrent_url", payloads.TorrentURL, "torrent_info_hash", payloads.TorrentInfoHash,
				"nzb_url", payloads.NZBURL, "gone_url", payloads.GoneURL)
			srv := &http.Server{
				Addr:              addr,
				Handler:           torznabstub.NewHandler(logPath, logger, payloads),
				ReadHeaderTimeout: 10 * time.Second,
			}
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":8080", "listen address")
	cmd.Flags().StringVar(&logPath, "request-log", "/data/.e2e-fixtures/torznab/requests.jsonl",
		"JSONL file on the shared /data volume that every non-probe request is appended to")
	cmd.Flags().StringVar(&payloads.TorrentURL, "torrent-url", "http://seeder.clustarr-system.svc/fixture.torrent",
		"the seeder's .torrent, which the torrent release links to (empty drops the release)")
	cmd.Flags().StringVar(&torrentContent, "torrent-content", seed.BakedClipPath,
		"the file the seeder seeds (its --content-path), hashed for the torrent release's infohash attr; "+
			"empty advertises no hash")
	cmd.Flags().StringVar(&payloads.NZBURL, "nzb-url", "http://nntp-stub-a.clustarr-system.svc/fixture.nzb",
		"an NNTP stub's .nzb, which the usenet release links to (empty drops the release)")
	cmd.Flags().StringVar(&payloads.GoneURL, "gone-url",
		"http://torznab-stub.clustarr-system.svc"+torznabstub.PathGone+torznabstub.GUIDGone+".torrent",
		"this stub's own 404 route, which the gone release links to (empty drops the release)")
	return cmd
}
