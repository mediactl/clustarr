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

	"github.com/mediactl/clustarr/test/fixtures/cardigannstub"
)

func newCardigannStubCommand() *cobra.Command {
	var (
		addr        string
		recordedDir string
	)
	cmd := &cobra.Command{
		Use:   "cardigann-stub",
		Short: "Serve the fixture Cardigann tracker page (login-form.yml) on --addr",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
			logger.Info("cardigann-stub: listening", "addr", addr, "recorded_dir", recordedDir)
			srv := &http.Server{
				Addr:              addr,
				Handler:           cardigannstub.NewHandler(recordedDir, logger),
				ReadHeaderTimeout: 10 * time.Second,
			}
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":8080", "listen address")
	cmd.Flags().StringVar(&recordedDir, "recorded-dir", "/fixtures/testdata/cardigann",
		"directory holding test/data/cardigann's login-form.html")
	return cmd
}
