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

package usenet

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// TestUnpackCountsEveryByteItWrites: the extracting stage's progress is
// what writeArchiveEntry wrote, through the same unpackArchives entry point
// postProcess calls, so the count matches the files on disk exactly.
func TestUnpackCountsEveryByteItWrites(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "release.zip")
	f, err := os.Create(src)
	require.NoError(t, err)
	zw := zip.NewWriter(f)
	entries := map[string]int{"Release.2026.1080p.mkv": 3<<20 + 17, "release.nfo": 512}
	var want int64
	for name, n := range entries {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write(bytes.Repeat([]byte{'x'}, n))
		require.NoError(t, err)
		want += int64(n)
	}
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())

	progress := &unpackProgress{}
	out := filepath.Join(dir, "out")
	res, err := unpackArchives(context.Background(), dir, out, "",
		[]nzbFile{{Name: "release.zip", Kind: kindArchive}}, progress)
	require.NoError(t, err)
	require.Equal(t, len(entries), res.Extracted)
	require.Equal(t, want, progress.Written())

	var onDisk int64
	for name := range entries {
		st, err := os.Stat(filepath.Join(out, name))
		require.NoError(t, err)
		onDisk += st.Size()
	}
	require.Equal(t, onDisk, progress.Written(), "the count is what landed on disk")

	var none *unpackProgress
	require.Zero(t, none.Written(), "a nil counter counts nothing and does not panic")
}

// TestItemReportsPostProcessingRatherThanAStaleTransfer pins what status
// says while par2 and the unpack run. On 2026-10-07 a 20 GB set spent 13
// minutes in par2 and 29 extracting over NFS with progressPercent at 99,
// lastProgressAt frozen and downloadRateBps still at the transfer's last
// 3 MB/s: it read as a stall. Now the message says what runs, for how long
// and, extracting, how much is written, and the rate and ETA are the
// transfer's alone.
func TestItemReportsPostProcessingRatherThanAStaleTransfer(t *testing.T) {
	c, _, _ := newTestClient(t, Config{Providers: []Provider{newStubServer(t).provider("solo", 1, 1)}})
	now := time.Now()
	j := &job{client: c, id: "round-midnight", dir: t.TempDir(), nzb: &nzbJob{
		Files: []nzbFile{
			{Name: "rm.part001.rar", Kind: kindArchive, Bytes: 10_000_000_000},
			{Name: "rm.part002.rar", Kind: kindArchive, Bytes: 10_221_000_000},
			{Name: "rm.vol000+01.par2", Kind: kindPar2Volume, Bytes: 50_000_000, Blocks: 1},
		},
		TotalBytes:    20_271_000_000,
		TotalSegments: 27_703,
	}}
	j.downloaded = j.nzb.TotalBytes - 768_000 // one article never arrived
	j.rate.observe(0, now.Add(-20*time.Second))
	j.rate.observe(30_000_000, now.Add(-10*time.Second)) // the transfer's last sample: 3 MB/s

	j.setStage(downloadv1alpha1.DownloadStageTransferring, download.StatusDownloading)
	it := j.item()
	require.EqualValues(t, 3_000_000, it.DownRate, "while transferring the rate is the transfer's")
	require.NotNil(t, it.ETA)
	require.Empty(t, it.Message)

	j.setStage(downloadv1alpha1.DownloadStageRepairing, download.StatusDownloading)
	j.mu.Lock()
	j.stageStarted = now.Add(-4*time.Minute - 12*time.Second)
	j.mu.Unlock()
	it = j.item()
	require.Zero(t, it.DownRate, "par2 downloads nothing; the transfer's last rate must not stand")
	require.Nil(t, it.ETA)
	require.Equal(t, "verifying and repairing with par2 for 4m12s", it.Message)

	progress := &unpackProgress{}
	progress.written.Store(16_380_000_000)
	j.mu.Lock()
	j.unpack, j.archiveBytes = progress, archiveBytes(j.nzb.Files)
	j.mu.Unlock()
	j.setStage(downloadv1alpha1.DownloadStageExtracting, download.StatusDownloading)
	j.mu.Lock()
	j.stageStarted = now.Add(-23 * time.Minute)
	j.mu.Unlock()
	it = j.item()
	require.Zero(t, it.DownRate)
	require.Nil(t, it.ETA)
	require.EqualValues(t, 99, it.ProgressPercent, "progress stays the download's")
	require.Equal(t, "extracting: 16.4 GB written of about 20.2 GB for 23m0s, 11.9 MB/s", it.Message)

	progress.written.Add(1_000_000_000)
	require.Contains(t, j.item().Message, "extracting: 17.4 GB written", "the message follows the unpack")

	j.mu.Lock()
	j.message = "usenet: write /data/x: no space left on device"
	j.mu.Unlock()
	require.Equal(t, "usenet: write /data/x: no space left on device", j.item().Message,
		"an engine's own message, a failure's, is never replaced")
}

// TestSetStageRecordsWhenAStageBegan: the elapsed time is the stage's, so
// setting the stage it is already in does not restart it.
func TestSetStageRecordsWhenAStageBegan(t *testing.T) {
	c, _, _ := newTestClient(t, Config{Providers: []Provider{newStubServer(t).provider("solo", 1, 1)}})
	j := &job{client: c, id: "x", dir: t.TempDir(), nzb: &nzbJob{}}
	j.setStage(downloadv1alpha1.DownloadStageRepairing, download.StatusDownloading)
	j.mu.Lock()
	first := j.stageStarted
	j.mu.Unlock()
	require.False(t, first.IsZero())

	j.setStage(downloadv1alpha1.DownloadStageRepairing, download.StatusDownloading)
	j.mu.Lock()
	require.Equal(t, first, j.stageStarted, "the same stage again keeps its start")
	j.mu.Unlock()

	j.setStage(downloadv1alpha1.DownloadStageExtracting, download.StatusDownloading)
	j.mu.Lock()
	require.False(t, j.stageStarted.Before(first), "a new stage starts its own clock")
	j.mu.Unlock()
}
