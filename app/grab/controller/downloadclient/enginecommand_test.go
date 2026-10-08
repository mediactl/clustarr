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

package downloadclient

import (
	"testing"

	"github.com/stretchr/testify/assert"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/binpath"
)

// TestEngineCommandRunsTheAgentWithOneDomain: every engine pod runs the
// agent binary with exactly one domain and its directories as flags -- no
// shell, and no identity taken from one: the pod derives its ordinal from
// $POD_NAME in Go (spec 2026-10-06 §3.5.5, W5.5).
func TestEngineCommandRunsTheAgentWithOneDomain(t *testing.T) {
	scratched := torrentClient("my-torrent-client", 2)
	scratched.Spec.Torrent.Scratch = &downloadv1alpha1.ScratchSpec{Path: "/data/torrents/incomplete"}
	published := usenetClient("nzb")
	published.Spec.Usenet.PublishDir = "/data/usenet/complete"
	for name, tc := range map[string]struct {
		dc   *downloadv1alpha1.DownloadClient
		want []string
	}{
		"torrent": {torrentClient("sab", 1), []string{"--domain=torrent-engine", "--download-client=sab", "--data-dir=/data"}},
		"torrent scratch": {scratched, []string{
			"--domain=torrent-engine", "--download-client=my-torrent-client",
			"--data-dir=/data", "--scratch-dir=/data/torrents/incomplete",
		}},
		"usenet": {usenetClient("nzb"), []string{"--domain=usenet-engine", "--engine=nzb-0", "--data-dir=/data", "--scratch-dir=/scratch"}},
		"usenet publish": {published, []string{
			"--domain=usenet-engine", "--engine=nzb-0", "--data-dir=/data",
			"--scratch-dir=/scratch", "--publish-dir=/data/usenet/complete",
		}},
	} {
		t.Run(name, func(t *testing.T) {
			cmd, args := EngineCommand(tc.dc, "/data", "/scratch")
			assert.Equal(t, []string{binpath.Agent}, cmd)
			assert.Equal(t, tc.want, args)
			for _, a := range append(cmd, args...) {
				assert.NotContains(t, a, "/bin/sh")
				assert.NotContains(t, a, "HOSTNAME")
			}
		})
	}
}
