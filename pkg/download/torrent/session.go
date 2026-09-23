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
	"fmt"
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
// [session.onWriteChunkError] and [session.checkStallLocked], both of which
// also call DisallowDataDownload, so a failed torrent cannot actually reach
// Complete afterwards -- the ordering is defensive, not load-bearing.
//
// # Two verdicts are reached here, not merely reported
//
// The stall ([session.checkStallLocked]) and the seed goal
// ([session.seedGoalMetLocked]) are both judged over time, and this is the
// one place the engine looks at a transfer on every poll, so this is where
// each is reached -- once, stickily -- the same way lastProgressAt is
// tracked here.
func (c *Client) itemFromTorrent(id string, t *anatorrent.Torrent) download.Item {
	sess := c.sessionFor(id)
	sess.mu.Lock()
	defer sess.mu.Unlock()

	now := time.Now()
	info := t.Info()

	// A torrent downloads in place: its per-transfer directory is both the
	// content root and the published output from the moment it is added,
	// which is what lets the Download controller's removeDataOnDelete
	// finalizer remove it even once this engine is gone (ruling R-6).
	item := download.Item{
		ID:          id,
		ContentRoot: sess.contentRoot,
		OutputPath:  sess.contentRoot,
		AddedAt:     sess.addedAt,
	}

	// Until the selection is applied (for a magnet, until its metadata has
	// arrived and [applySelection] has run) nothing is wanted yet, so the
	// torrent is reported exactly as one still fetching metadata.
	selecting := info != nil && !sess.selectionApplied

	var (
		complete                                    bool
		totalBytes, downloadedBytes, remainingBytes int64
	)
	switch {
	case info == nil || selecting:
	case sess.wanted == nil:
		complete = t.Complete().Bool()
		totalBytes = info.TotalLength()
		downloadedBytes = t.BytesCompleted()
		remainingBytes = t.BytesMissing()
		for _, f := range t.Files() {
			item.Files = append(item.Files, download.File{Path: f.Path(), SizeBytes: f.Length()})
		}
	default:
		complete, totalBytes, downloadedBytes, item.Files = selectedProgress(t, sess.wanted)
		remainingBytes = totalBytes - downloadedBytes
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
	if uploadedBytes > sess.lastUploadBytes {
		sess.lastUploadAt = now
	}
	sess.lastUploadBytes = uploadedBytes

	if complete && !sess.seedGoalMet && sess.seedGoalMetLocked(uploadedBytes, downloadedBytes, now) {
		// Design spec §6.3: "seed criteria ... -> DisallowDataUpload(),
		// CanBeRemoved=true, SeedGoalMet". Stopping the upload is qBittorrent's
		// default share-limit action (pause the torrent), and it is what
		// makes a met goal stay met.
		sess.seedGoalMet = true
		sess.seedGoalMetAt = now
		t.DisallowDataUpload()
	}
	if complete {
		until := now
		if sess.seedGoalMet {
			until = sess.seedGoalMetAt
		}
		item.SeedTime = until.Sub(sess.completedAt)
	} else {
		sess.checkStallLocked(t.DisallowDataDownload, c.cfg.StallTimeout, now)
	}

	// CanMoveFiles/CanBeRemoved must reflect real torrent state, not
	// optimism (see the Client.Add doc comment): a torrent is only
	// importable once every wanted byte is verified on disk, and only
	// removable once it has been imported AND (for a torrent, unlike
	// usenet) its seed goal is met. SeedGoalMet reports the goal on its
	// own, import or not, so the controller can record it.
	item.CanMoveFiles = complete
	item.SeedGoalMet = complete && sess.seedGoalMet
	item.CanBeRemoved = item.SeedGoalMet && sess.imported

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
	case info == nil || selecting:
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

// selectedProgress measures a torrent with a partial file selection over its
// WANTED files only -- [download.Item.TotalBytes]'s own contract: "the size
// of the wanted content -- the selected files, not the whole torrent" --
// and lists every file, the unwanted ones as Skipped. wanted is indexed like
// t.Files().
//
// anacrolix's own Complete, BytesCompleted and BytesMissing all count the
// whole torrent, so none of them can say a partial selection is finished:
// Complete never turns on while an unwanted file is missing. Completion is
// therefore every piece overlapping a wanted file verified complete. The
// per-file byte count is checked first because it is one call per file and
// is already short of the file's length for any file still transferring,
// so the per-piece walk only runs once the wanted bytes are all present.
// (File.BytesCompleted also counts written-but-unverified chunks, which is
// why it can rule completion out but not in.)
func selectedProgress(t *anatorrent.Torrent, wanted []bool) (complete bool, total, done int64, files []download.File) {
	all := t.Files()
	files = make([]download.File, 0, len(all))
	complete = true
	for i, f := range all {
		want := i < len(wanted) && wanted[i]
		files = append(files, download.File{Path: f.Path(), SizeBytes: f.Length(), Skipped: !want})
		if !want {
			continue
		}
		have := f.BytesCompleted()
		total += f.Length()
		done += have
		if have < f.Length() {
			complete = false
		}
	}
	if !complete {
		return false, total, done, files
	}
	for i, f := range all {
		if i >= len(wanted) || !wanted[i] {
			continue
		}
		for p := f.BeginPieceIndex(); p < f.EndPieceIndex(); p++ {
			if !t.PieceState(p).Complete {
				return false, total, done, files
			}
		}
	}
	return true, total, done, files
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

// checkStallLocked fails an unfinished transfer that has downloaded nothing
// for timeout: qBittorrent's stalledDL ("no data is being received"), which
// Sonarr reports as a Warning -- "The download is stalled with no
// connections" -- held for the whole window. The caller must hold s.mu and
// must not call it for a complete transfer. A timeout of zero disables it.
//
// The window is measured from the later of the last progress and
// [session.activeSince], so a pause is never counted (a paused transfer is
// skipped outright, and Resume restarts the clock) and neither is an engine
// restart: lastProgressAt lives in memory, and a re-attach sets activeSince.
// A magnet whose metadata never arrives makes no progress either, and
// stalls the same way -- qBittorrent's metaDL with nobody to ask.
//
// The verdict is sticky, and the transfer stops requesting data (stop is
// the torrent's DisallowDataDownload), exactly as
// [session.onWriteChunkError] does: a stalled transfer the controller is
// about to blocklist must not quietly finish afterwards.
func (s *session) checkStallLocked(stop func(), timeout time.Duration, now time.Time) {
	if timeout <= 0 || s.failed || s.paused || s.activeSince.IsZero() {
		return
	}
	since := s.activeSince
	if s.hasProgress && s.lastProgressAt.After(since) {
		since = s.lastProgressAt
	}
	if now.Sub(since) < timeout {
		return
	}
	stop()
	s.failed = true
	s.failureReason = downloadv1alpha1.DownloadFailureStalled
	s.message = fmt.Sprintf("stalled: no data received for %s (stall timeout %s)",
		now.Sub(since).Truncate(time.Second), timeout)
}

// seedGoalMetLocked reports whether sess's seed criteria are satisfied. The
// caller must hold s.mu and must only call this once the transfer is
// complete -- an incomplete transfer has nothing to evaluate a seed goal
// against.
//
// Any one limit meets the goal, as in qBittorrent's share limits, which are
// the three Sonarr's qBittorrent client checks for HasReachedSeedLimit
// (docs/research/download.md): ratio, seeding time, and inactive seeding
// time -- here SeedCriteria.InactiveTime, measured from the later of the
// completion and the last upload.
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

	if sc.InactiveTime != nil && sc.InactiveTime.Duration > 0 && !s.completedAt.IsZero() {
		idleSince := s.completedAt
		if s.lastUploadAt.After(idleSince) {
			idleSince = s.lastUploadAt
		}
		if now.Sub(idleSince) >= sc.InactiveTime.Duration {
			return true
		}
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

		reason := chunkWriteFailure(err)

		s.mu.Lock()
		s.failed = true
		s.failureReason = reason
		s.message = err.Error()
		s.mu.Unlock()
	}
}

// chunkWriteFailure classifies a storage write error: diskFull when the
// volume is out of space (ENOSPC) or the writer out of quota (EDQUOT, the
// same condition as far as a download is concerned), writeError for
// anything else. Both are local faults, so neither blocklists the release.
func chunkWriteFailure(err error) downloadv1alpha1.DownloadFailureReason {
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
		return downloadv1alpha1.DownloadFailureDiskFull
	}
	return downloadv1alpha1.DownloadFailureWriteError
}
