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

package torrent

import (
	"errors"
	"syscall"
	"time"

	anatorrent "github.com/anacrolix/torrent"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// itemFromTorrent renders t as a [download.Item], the one place this package
// maps anacrolix's own view of a transfer plus this package's session
// bookkeeping onto the interface's shared vocabulary.
//
// # Status and Stage
//
// A torrent that is fetching metadata or checking existing data is
// deliberately not StatusDownloading -- see the [download.Client] doc
// comment on the Add method for why that mapping matters. The precedence
// order below (failed, then complete, then paused, then metadata/checking,
// then peerless, then downloading) means a torrent that finishes while
// marked failed still reports StatusFailed: [session.failed] is set only by
// [session.onWriteChunkError], which also calls DisallowDataDownload, so a
// failed torrent cannot actually reach Complete afterwards -- the ordering
// is defensive, not load-bearing.
func (c *Client) itemFromTorrent(id string, t *anatorrent.Torrent) download.Item {
	sess := c.sessionFor(id)
	sess.mu.Lock()
	defer sess.mu.Unlock()

	now := time.Now()
	complete := t.Complete().Bool()
	info := t.Info()

	item := download.Item{ID: id, ContentRoot: sess.contentRoot}

	var totalBytes, downloadedBytes, remainingBytes int64
	if info != nil {
		totalBytes = info.TotalLength()
		downloadedBytes = t.BytesCompleted()
		remainingBytes = t.BytesMissing()
		for _, f := range t.Files() {
			item.Files = append(item.Files, download.File{
				Path:      f.Path(),
				SizeBytes: f.Length(),
			})
		}
	}

	stats := t.Stats()
	uploadedBytes := stats.BytesWrittenData.Int64()

	item.TotalBytes = totalBytes
	item.DownloadedBytes = downloadedBytes
	item.RemainingBytes = remainingBytes
	item.UploadedBytes = uploadedBytes
	item.Seeders = stats.ConnectedSeeders
	item.Peers = stats.ActivePeers
	item.ProgressPercent = progressPercent(downloadedBytes, totalBytes)
	item.RatioMilli = ratioMilli(uploadedBytes, downloadedBytes)
	item.DownRate, item.UpRate = sess.sampleRatesLocked(now, downloadedBytes, uploadedBytes)

	if downloadedBytes > sess.lastProgressBytes {
		sess.lastProgressAt = now
		sess.hasProgress = true
	}
	sess.lastProgressBytes = downloadedBytes
	if sess.hasProgress {
		lp := sess.lastProgressAt
		item.LastProgressAt = &lp
	}

	if complete && sess.completedAt.IsZero() {
		sess.completedAt = now
	}

	// CanMoveFiles/CanBeRemoved must reflect real torrent state, not
	// optimism (see the Client.Add doc comment): a torrent is only
	// importable once every wanted byte is verified on disk, and only
	// removable once it has been imported AND (for a torrent, unlike
	// usenet) its seed goal is met.
	item.CanMoveFiles = complete
	item.CanBeRemoved = complete && sess.imported && sess.seedGoalMetLocked(uploadedBytes, downloadedBytes, now)

	switch {
	case sess.failed:
		item.Status = download.StatusFailed
		item.FailureReason = sess.failureReason
		item.Message = sess.message
	case complete:
		item.Status = download.StatusCompleted
		if !sess.paused && t.Seeding() {
			item.Stage = downloadv1alpha1.DownloadStageSeeding
		} else {
			item.Stage = downloadv1alpha1.DownloadStageDone
		}
	case sess.paused:
		// A paused transfer reports StatusPaused regardless of what stage it
		// would otherwise be in: Pause's contract is "transfers nothing",
		// and a Stage left over from before the pause would say otherwise.
		item.Status = download.StatusPaused
	case info == nil:
		item.Status = download.StatusQueued
		item.Stage = downloadv1alpha1.DownloadStageFetchingMetadata
	case isChecking(t):
		item.Status = download.StatusQueued
		item.Stage = downloadv1alpha1.DownloadStageVerifying
	case stats.ActivePeers == 0:
		item.Status = download.StatusWarning
		item.Stage = downloadv1alpha1.DownloadStageTransferring
		item.Message = "no peers connected"
	default:
		item.Status = download.StatusDownloading
		item.Stage = downloadv1alpha1.DownloadStageTransferring
	}

	return item
}

// isChecking reports whether any of t's pieces are being hashed or are
// queued to be, which anacrolix calls "checking" -- re-verifying data that
// was already on disk, as opposed to fetching new data. A torrent in this
// state is not StatusDownloading even though bytes may be moving (disk
// reads for the hash), so it gets its own DownloadStageVerifying instead.
func isChecking(t *anatorrent.Torrent) bool {
	for _, run := range t.PieceStateRuns() {
		if run.Checking && run.Length > 0 {
			return true
		}
	}
	return false
}

// sampleRatesLocked derives a bytes-per-second rate from the change in
// downloadedBytes/uploadedBytes since the last sample. The caller must hold
// s.mu.
//
// anacrolix reports only cumulative counters, never an instantaneous rate,
// so this is the only source for Item.DownRate/UpRate. The one-second floor
// exists because Get and List are called at whatever cadence the engine
// runnable polls on (D2-5); a sample taken milliseconds after the last one
// would divide a real byte count by a near-zero duration and report a wildly
// inflated rate for one tick. Below that floor the previous rate is repeated
// rather than recomputed, which is a stale reading, not a wrong one.
func (s *session) sampleRatesLocked(now time.Time, downloadedBytes, uploadedBytes int64) (downRate, upRate int64) {
	if !s.haveSample {
		s.haveSample = true
		s.lastSampleAt = now
		s.lastDownloaded = downloadedBytes
		s.lastUploaded = uploadedBytes
		return 0, 0
	}

	elapsed := now.Sub(s.lastSampleAt)
	if elapsed < time.Second {
		return s.downRate, s.upRate
	}

	s.downRate = bytesPerSecond(downloadedBytes-s.lastDownloaded, elapsed)
	s.upRate = bytesPerSecond(uploadedBytes-s.lastUploaded, elapsed)
	s.lastSampleAt = now
	s.lastDownloaded = downloadedBytes
	s.lastUploaded = uploadedBytes
	return s.downRate, s.upRate
}

// seedGoalMetLocked reports whether sess's seed criteria are satisfied. The
// caller must hold s.mu and must only call this once the transfer is
// complete -- an incomplete transfer has nothing to evaluate a seed goal
// against.
//
// AddRequest.SeedCriteria is documented as "already merged from the client
// default and the Download's override", which this package reads as: the
// caller (the Download controller, D2-4) has already resolved which of
// SeedTime and PackSeedTime applies to this specific transfer, so this
// method does not try to re-derive pack-ness from file count. SeedTime is
// preferred when both are set, since a caller that means "pack" would send
// PackSeedTime alone.
func (s *session) seedGoalMetLocked(uploadedBytes, downloadedBytes int64, now time.Time) bool {
	if !s.hasSeedCriteria {
		return true
	}
	sc := s.seedCriteria

	if sc.Ratio != nil && downloadedBytes > 0 {
		if uploadedBytes*1000/downloadedBytes >= sc.Ratio.MilliValue() {
			return true
		}
	}

	target := sc.SeedTime
	if target == nil {
		target = sc.PackSeedTime
	}
	if target != nil && !s.completedAt.IsZero() && now.Sub(s.completedAt) >= target.Duration {
		return true
	}

	return false
}

// onWriteChunkError returns anacrolix's storage-write-failure hook for t.
//
// Setting Torrent.SetOnWriteChunkError replaces anacrolix's own default
// handler entirely (see the doc comment on Torrent.onWriteChunkErr), whose
// job is to disable further data download so a failing disk does not spin
// forever. This closure preserves that call and additionally marks the
// session failed, which the default handler has no way to do since it does
// not know about [session] at all -- without this, a write failure would
// silently degrade a transfer to "stalled forever" (StatusWarning, no
// peers... eventually) instead of the StatusFailed the controller can act
// on.
func (s *session) onWriteChunkError(t *anatorrent.Torrent) func(error) {
	return func(err error) {
		t.DisallowDataDownload()

		reason := downloadv1alpha1.DownloadFailureWriteError
		if errors.Is(err, syscall.ENOSPC) {
			reason = downloadv1alpha1.DownloadFailureDiskFull
		}

		s.mu.Lock()
		s.failed = true
		s.failureReason = reason
		s.message = err.Error()
		s.mu.Unlock()
	}
}
