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

// Package markers keeps MediaFile status.markers: a file's skip segments
// from TheIntroDB (spec 2026-09-30 plex-analyze-bypass §3). Due decides
// when a file needs fetching; Handler fetches and records.
package markers

import (
	"strconv"
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
)

// How long each result stands before the file is asked about again.
const (
	FoundTTL    = 30 * 24 * time.Hour
	NotFoundTTL = 7 * 24 * time.Hour
	ErrorTTL    = 24 * time.Hour
)

// Due reports whether mf needs its markers fetched now and, when it does
// not, how long until it will. Only a probed movie or episode file has
// markers: the probe's duration is what identifies its release.
func Due(mf *catalogv1alpha1.MediaFile, now time.Time) (bool, time.Duration) {
	switch mf.Spec.MediaRef.Kind {
	case commonv1.MediaKindMovie, commonv1.MediaKindEpisode:
	default:
		return false, 0
	}
	if mf.Status.MediaInfo == nil || mf.Status.ProbeHash == "" {
		return false, 0
	}
	m := mf.Status.Markers
	if m == nil || m.ForProbeHash != mf.Status.ProbeHash {
		return true, 0
	}
	ttl := ErrorTTL
	switch m.Result {
	case catalogv1alpha1.MarkersFound:
		ttl = FoundTTL
	case catalogv1alpha1.MarkersNotFound:
		ttl = NotFoundTTL
	}
	left := m.FetchedAt.Add(ttl).Sub(now)
	if left <= 0 {
		return true, 0
	}
	return false, left
}

// MsgID is the fetch task's deduplication id: the file, its probe and the
// fetch it replaces, so a republish of one pending fetch is absorbed and
// the next scheduled fetch is not.
func MsgID(mf *catalogv1alpha1.MediaFile) string {
	var last int64
	if mf.Status.Markers != nil {
		last = mf.Status.Markers.FetchedAt.Unix()
		if mf.Status.Markers.FetchedAt.IsZero() {
			last = 0
		}
	}
	return events.MsgIDForObject(string(mf.UID), 0, "markers-"+mf.Status.ProbeHash+"-"+strconv.FormatInt(last, 10))
}
