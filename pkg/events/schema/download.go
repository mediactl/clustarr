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

package schema

import (
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// DownloadEvent reports a transition of a Download. Subject:
// clustarr.evt.download.download.<action>.<uid>.
type DownloadEvent struct {
	// DownloadRef is the Download the event is about.
	DownloadRef Ref `json:"downloadRef"`

	// Media identifies the catalog item the download is for.
	Media commonv1.MediaRef `json:"media"`

	// Action is one of queued, started, completed, seedGoalMet, imported,
	// failed, blocklisted or removed.
	Action string `json:"action"`

	// ClientRef is the DownloadClient handling the transfer.
	ClientRef *Ref `json:"clientRef,omitempty"`

	// ClientID is the download identifier inside the client.
	ClientID string `json:"clientID,omitempty"`

	// Protocol is the transfer protocol.
	Protocol commonv1.Protocol `json:"protocol,omitempty"`

	// Title is the release title.
	Title string `json:"title,omitempty"`

	// InfoHash is the torrent info hash, for blocklisting.
	InfoHash string `json:"infoHash,omitempty"`

	// SizeBytes is the total transfer size.
	SizeBytes int64 `json:"sizeBytes,omitempty"`

	// Reason explains a failure, blocklisting or removal.
	Reason string `json:"reason,omitempty"`

	// OutputPath is where the client left the finished data.
	OutputPath string `json:"outputPath,omitempty"`

	// At is when the transition happened.
	At time.Time `json:"at"`
}

// Schema implements Payload.
func (DownloadEvent) Schema() string { return "download.DownloadEvent.v1" }

// DownloadProgress is 1 Hz telemetry published on core NATS at
// clustarr.progress.download.<uid> and mirrored into the clustarr-progress
// key/value bucket. It is never persisted to a stream.
//
// Fractional values are carried as scaled integers so the payload stays
// exact and matches the integer fields on Download status.
type DownloadProgress struct {
	// DownloadRef is the Download this telemetry is for.
	DownloadRef Ref `json:"downloadRef"`

	// Status is the client-reported status.
	Status string `json:"status,omitempty"`

	// Stage is the client-reported stage, e.g. "downloading" or "repairing".
	Stage string `json:"stage,omitempty"`

	// PercentMilli is completion in thousandths of a percent, 0..100000.
	PercentMilli int32 `json:"percentMilli"`

	// TotalBytes is the total transfer size.
	TotalBytes int64 `json:"totalBytes,omitempty"`

	// DownloadedBytes is how much has arrived.
	DownloadedBytes int64 `json:"downloadedBytes,omitempty"`

	// UploadedBytes is how much has been sent, for torrents.
	UploadedBytes int64 `json:"uploadedBytes,omitempty"`

	// DownRateBytesPerSec is the current download rate.
	DownRateBytesPerSec int64 `json:"downRateBytesPerSec,omitempty"`

	// UpRateBytesPerSec is the current upload rate.
	UpRateBytesPerSec int64 `json:"upRateBytesPerSec,omitempty"`

	// RatioMilli is the share ratio in thousandths, so 1.75 is 1750.
	RatioMilli int32 `json:"ratioMilli,omitempty"`

	// SeedSeconds is how long the torrent has been seeding.
	SeedSeconds int64 `json:"seedSeconds,omitempty"`

	// ETASeconds is the client's estimate of time remaining.
	ETASeconds int64 `json:"etaSeconds,omitempty"`

	// Seeders is the current seeder count.
	Seeders int32 `json:"seeders,omitempty"`

	// Peers is the current peer count.
	Peers int32 `json:"peers,omitempty"`

	// At is when the sample was taken.
	At time.Time `json:"at"`
}

// Schema implements Payload.
func (DownloadProgress) Schema() string { return "download.Progress.v1" }
