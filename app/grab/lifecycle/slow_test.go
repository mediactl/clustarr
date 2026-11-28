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

package lifecycle_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
)

// slowView is a torrent that started ago ago, holds done of total bytes and
// last progressed a minute ago: by the stall timeout it is never stalled.
func slowView(ago time.Duration, done, total int64, stage commonv1.DownloadStage) lifecycle.View {
	e := entry(commonv1.DownloadPhaseDownloading)
	tr := record(e, seq0, stage)
	started := now.Add(-ago)
	tr.Record.StartedAt, tr.Record.AddedAt = &started, started
	tr.Record.TotalBytes, tr.Record.RemainingBytes, tr.Record.DownloadedBytes = total, total-done, done
	return view([]catalogv1alpha1.DownloadEntry{e}, tr)
}

// A torrent that trickles is never stalled, each byte being progress, and
// held its episodes as downloading for days: on talos (2026-11-28) a 200 GB
// remux stood at 31 GB after 23.5 hours, five more days at that rate, among
// fifteen torrents moving 0.4 MB/s between them. Past SlowAfter a torrent
// that would need more than SlowHorizon at the rate it has averaged is
// failed as stalled and blocked, so the next release is searched for.
func TestATorrentTooSlowToFinishIsFailedAsStalled(t *testing.T) {
	const gb = int64(1) << 30
	for name, tc := range map[string]struct {
		view lifecycle.View
		slow bool
	}{
		"200 GB at 31 GB after 23.5 h":          {slowView(23*time.Hour+30*time.Minute, 31*gb, 200*gb, commonv1.DownloadStageTransferring), true},
		"62 GB at 11 GB after 23 h":             {slowView(23*time.Hour, 11*gb, 62*gb, commonv1.DownloadStageTransferring), true},
		"nothing after 13 h":                    {slowView(13*time.Hour, 0, 10*gb, commonv1.DownloadStageTransferring), true},
		"86 GB at 45 GB after 11 h: too early":  {slowView(11*time.Hour, 45*gb, 86*gb, commonv1.DownloadStageTransferring), false},
		"67 GB at 44 GB after 22 h: 11 h to go": {slowView(22*time.Hour, 44*gb, 67*gb, commonv1.DownloadStageTransferring), false},
		"a slow one the engine is verifying":    {slowView(23*time.Hour, 1*gb, 200*gb, commonv1.DownloadStageVerifying), false},
		"a slow one eleven hours in is not yet": {slowView(11*time.Hour, 1*gb, 200*gb, commonv1.DownloadStageTransferring), false},
	} {
		t.Run(name, func(t *testing.T) {
			p := lifecycle.Decide(tc.view)
			e := only(p)
			if !tc.slow {
				assert.Equal(t, commonv1.DownloadPhaseDownloading, e.Phase, e.Message)
				return
			}
			require.Equal(t, commonv1.DownloadPhaseBlocklisted, e.Phase, e.Message)
			assert.Equal(t, commonv1.DownloadFailureStalled, e.FailureReason)
			assert.Contains(t, e.Message, "too slow")
		})
	}
}
