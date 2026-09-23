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
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/mediactl/clustarr/test/fixtures/gestdownstub"
)

func newGestdownStubCommand() *cobra.Command {
	var (
		addr    string
		logPath string
	)
	cmd := &cobra.Command{
		Use:   "gestdown-stub",
		Short: "Serve the fixture Gestdown (api.gestdown.info) upstream on --addr",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
			logger.Info("gestdown-stub: listening", "addr", addr, "request_log", logPath)
			srv := &http.Server{
				Addr:              addr,
				Handler:           gestdownstub.NewHandler(logPath, logger),
				ReadHeaderTimeout: 10 * time.Second,
			}
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":8080", "listen address")
	cmd.Flags().StringVar(&logPath, "request-log", "/data/.e2e-fixtures/gestdown/requests.jsonl",
		"JSONL file on the shared /data volume that every non-probe request is appended to")
	return cmd
}
