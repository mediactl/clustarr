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
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mediactl/clustarr/test/fixtures/nntpstub"
	"github.com/mediactl/clustarr/test/fixtures/seed"
)

func newNNTPStubCommand() *cobra.Command {
	var (
		addr          string
		httpAddr      string
		deny          []string
		denyStatus    int
		user, pass    string
		segmentBytes  int
		segmentCount  int
		requestLogDir string
		contentPath   string
	)
	cmd := &cobra.Command{
		Use:   "nntp-stub",
		Short: "Serve the fixture NNTP server on --addr, and its NZB over HTTP on --http-addr",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

			var fx nntpstub.Fixture
			if contentPath != "" {
				built, err := nntpstub.BuildFromFile(contentPath, segmentBytes)
				if err != nil {
					return err
				}
				fx = built
			} else {
				fx = nntpstub.Build(segmentBytes, segmentCount)
			}
			denyMap := make(map[string]int, len(deny))
			for _, id := range deny {
				id = strings.TrimSpace(id)
				if id == "" {
					continue
				}
				denyMap[id] = denyStatus
			}

			ids := make([]string, len(fx.Articles))
			for i, a := range fx.Articles {
				ids[i] = a.ID
			}

			srv, err := nntpstub.NewServer(fx, nntpstub.Options{
				Addr:       addr,
				Deny:       denyMap,
				Username:   user,
				Password:   pass,
				RequestLog: requestLogDir,
				Logger:     logger,
			})
			if err != nil {
				return err
			}
			logger.Info("nntp-stub: listening", "addr", srv.Addr(), "title", fx.Title,
				"articles", ids, "denied", deny, "request_log", requestLogDir)

			errCh := make(chan error, 2)
			go func() { errCh <- srv.Serve(context.Background()) }()

			if httpAddr != "" {
				mux := http.NewServeMux()
				mux.Handle("GET /fixture.nzb", nntpstub.NZBHandler(fx))
				httpSrv := &http.Server{Addr: httpAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
				logger.Info("nntp-stub: serving nzb over http", "addr", httpAddr)
				go func() {
					if lErr := httpSrv.ListenAndServe(); lErr != nil && !errors.Is(lErr, http.ErrServerClosed) {
						errCh <- lErr
						return
					}
					errCh <- nil
				}()
			}

			return <-errCh
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":1119", "NNTP listen address")
	cmd.Flags().StringVar(&httpAddr, "http-addr", ":8080", "HTTP listen address for the fixture's .nzb file (empty disables)")
	cmd.Flags().StringSliceVar(&deny, "deny", nil,
		"message-ids (no angle brackets) this instance refuses with --deny-status; "+
			"run a second instance with a different --deny (or none) to exercise 430 cross-server failover")
	cmd.Flags().IntVar(&denyStatus, "deny-status", nntpstub.DefaultDenyStatus, "status code returned for a denied article")
	cmd.Flags().StringVar(&user, "user", "", "required AUTHINFO username (empty disables auth)")
	cmd.Flags().StringVar(&pass, "pass", "", "required AUTHINFO password")
	cmd.Flags().IntVar(&segmentBytes, "segment-bytes", nntpstub.DefaultSegmentBytes, "decoded size of one article")
	cmd.Flags().IntVar(&segmentCount, "segment-count", nntpstub.DefaultSegmentCount,
		"number of articles in the fixture's one file; ignored when --content-path is set, where the real "+
			"file's own length divides by --segment-bytes instead")
	cmd.Flags().StringVar(&requestLogDir, "request-log", "/data/.e2e-fixtures/nntp/requests.jsonl",
		"JSONL file on the shared /data volume every BODY/STAT is appended to")
	cmd.Flags().StringVar(&contentPath, "content-path", seed.BakedClipPath,
		"real media file split into --segment-bytes yEnc articles, so a completed transfer against this "+
			"fixture is real, ffprobe-able media rather than synthetic bytes (X12c, docs/superpowers/plans/"+
			"2026-09-23-gap-fixes.md); empty falls back to Build's synthetic --segment-bytes x --segment-count "+
			"payload, which is what this package's own tests use since they run outside the fixture image "+
			"and have no baked clip to point at")
	return cmd
}
