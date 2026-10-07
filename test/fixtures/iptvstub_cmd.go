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

	"github.com/mediactl/clustarr/test/fixtures/iptvstub"
)

func newIPTVStubCommand() *cobra.Command {
	var addr, base string
	cmd := &cobra.Command{
		Use:   "iptv-stub",
		Short: "Serve the fixture IPTV provider (playlist, guide, streams, /stats) on --addr",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
			logger.Info("iptv-stub: listening", "addr", addr, "base", base)
			srv := &http.Server{Addr: addr, Handler: iptvstub.NewHandler(base), ReadHeaderTimeout: 10 * time.Second}
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":8080", "listen address")
	cmd.Flags().StringVar(&base, "base", "http://iptv-stub.clustarr-system.svc", "the base the playlist's stream and guide URLs are built on")
	return cmd
}
