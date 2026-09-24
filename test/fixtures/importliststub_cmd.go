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

	"github.com/mediactl/clustarr/test/fixtures/importliststub"
)

func newImportListStubCommand() *cobra.Command {
	var (
		addr        string
		recordedDir string
	)
	cmd := &cobra.Command{
		Use:   "importlist-stub",
		Short: "Serve the fixture Trakt, Plex and mdblist import-list endpoints on --addr",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
			logger.Info("importlist-stub: listening", "addr", addr, "recorded_dir", recordedDir)
			srv := &http.Server{
				Addr:              addr,
				Handler:           importliststub.NewHandler(recordedDir, logger),
				ReadHeaderTimeout: 10 * time.Second,
			}
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":8080", "listen address")
	cmd.Flags().StringVar(&recordedDir, "recorded-dir", "/fixtures/testdata/importlist",
		"directory holding test/data/importlist's trakt, plex and mdblist subdirectories")
	return cmd
}
