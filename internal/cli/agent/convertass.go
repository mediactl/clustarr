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
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/mediactl/clustarr/pkg/subtitles/asssrt"
)

// newConvertASSCommand is `agent convert-ass-sidecars`, standard.Version 5's
// one-off runbook step: since then every sidecar the transcode writes is
// SubRip, because Plex's web player shows an ASS subtitle only by burning it
// into the video, a video transcode cluster-plex never allows (talos,
// 2026-10-20). It replaces the .ass sidecars earlier transcodes wrote with
// plain .srt ones (pkg/subtitles/asssrt.ConvertSidecars) and prints its
// report as JSON. It needs no domain, no cluster and no NATS: run it once
// as a Job on the media volume, as the import agent runs, then restart the
// manager so the subtitles planner lists every directory again.
func newConvertASSCommand() *cobra.Command {
	var root, backup string
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "convert-ass-sidecars",
		Short: "Replace every .ass subtitle sidecar under --root with a plain .srt one",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rep, err := asssrt.ConvertSidecars(root, backup, dryRun)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(rep)
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&root, "root", "/data/media", "The library to walk.")
	fs.StringVar(&backup, "backup-dir", "/data/.clustarr/ass-backup",
		"Where each replaced .ass goes, at its path relative to --root; clear it once the .srt files are checked.")
	fs.BoolVar(&dryRun, "dry-run", false, "Report what would be replaced and change nothing.")
	return cmd
}
