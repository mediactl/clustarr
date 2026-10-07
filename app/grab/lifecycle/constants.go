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

package lifecycle

import "time"

// The state machine's timings (ADR-0019 §6.4, §6.7, §6.8, §6.14; plan
// "Timeouts and windows").
const (
	// BlockQuarantine is how long a confirmed Blocklisted entry stays as a
	// tombstone after its transfer is removed, so no answer computed before
	// the block regrabs the release (§6.14).
	BlockQuarantine = time.Hour
	// UnclaimedGrace is how long after an engine boot or a transfer's add
	// claimed: false means nothing (§6.7).
	UnclaimedGrace = 10 * time.Minute
	// UnidentifiedGrace is how long a transfer with no claim at all waits
	// before its removal with bytes kept (§6.7).
	UnidentifiedGrace = 10 * time.Minute
	// EngineTeardownTimeout is R-6: how long an entry in Removing waits for
	// an engine that is gone (§6.8).
	EngineTeardownTimeout = 10 * time.Minute
	// ResyncSettle is how long after an engine reattached and reached the
	// asked resyncSeq an entry with no record is judged not held (§6.7).
	ResyncSettle = 2 * time.Minute
	// BlocklistTTL is every blocklist row's lifetime (§6.14).
	BlocklistTTL = 90 * 24 * time.Hour
	// BlockCallTimeout bounds the owed clustarr.rpc.indexarr.blocklist call.
	BlockCallTimeout = 5 * time.Second
	// ImportHoldRetention is how long a held import keeps its files before
	// it fails importExpired (CLAUDE.md, 2026-10-07).
	ImportHoldRetention = 24 * time.Hour
	// RedownloadWindow: a failure older than this starts no redownload (R25).
	RedownloadWindow = 24 * time.Hour
	// EngineRecordFresh is how young an engine record must be for its engine
	// to count as Ready.
	EngineRecordFresh = 2 * time.Minute
	// StallTimeout is the torrent stall default (DownloadClient
	// spec.torrent.stallTimeout's), moved from the engine (§3.5 P111): the
	// stall is the manager's judgement on the record's lastProgressAt.
	StallTimeout = 24 * time.Hour
	// RepublishWindow is records.RepublishWindow: an owed command is
	// republished under its Msg-Id inside it, and at a new seq past it,
	// below CLUSTARR_WORK_ENGINE's one-hour duplicate window.
	RepublishWindow = 50 * time.Minute
	// RepublishEvery is how often an owed command is republished.
	RepublishEvery = 30 * time.Second
	// MaxEntryMessage is entry.message's cap (the record keeps 2048).
	MaxEntryMessage = 1024
)

// History actions (pkg/events' Action*, restated: this package links no
// bus).
const (
	ActionGrabbed     = "grabbed"
	ActionQueued      = "queued"
	ActionStarted     = "started"
	ActionCompleted   = "completed"
	ActionSeedGoalMet = "seedGoalMet"
	ActionImported    = "imported"
	ActionFailed      = "failed"
	ActionBlocklisted = "blocklisted"
	ActionRemoved     = "removed"
)

// Event reasons (§7.7) and their recorders.
const (
	RecorderDownloads  = "downloads"
	RecorderGrabEngine = "grabarr-engine"

	ReasonDownloadAddFailed   = "DownloadAddFailed"
	ReasonEngineGone          = "EngineGone"
	ReasonSeedingLost         = "SeedingLost"
	ReasonCommandRetrying     = "CommandRetrying"
	ReasonInvalidAnnotation   = "InvalidAnnotation"
	ReasonNothingToRemove     = "NothingToRemove"
	ReasonNothingToResume     = "NothingToResume"
	ReasonTransferOwnerGone   = "TransferOwnerGone"
	ReasonUnidentifiedRemoved = "UnidentifiedTransferRemoved"

	EventNormal  = "Normal"
	EventWarning = "Warning"
)

// nextSeq is records.NextSeq: above the record's and the status's, and
// never below the clock's millisecond, so a seq never restarts when a TTL
// retires a record.
func nextSeq(recordSeq, statusSeq int64, now time.Time) int64 {
	return max(recordSeq+1, statusSeq+1, now.UnixMilli())
}
