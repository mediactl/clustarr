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
// from TheIntroDB (spec 2026-09-30 plex-analyze-bypass §3) merged with
// clustarr's own segment analysis. Since the fold (ADR-0016, loop spec
// 2026-10-06 §4.12) it is the protocol between the remediation loop and the
// metadata domain's marker worker: Due and DueAt decide when a file needs
// asking, Plan decides the status block and the request (pure), Records is
// the loop's read half of clustarr-markers, Answers the worker's write half,
// and TaskMessage and DeferredTask build the task. Nothing here writes
// MediaFile status: the loop applies what Plan decides.
package markers

import (
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// How long each result stands before the file is asked about again. A
// NotFound stands NotFoundTTL the first time, then longer the longer
// TheIntroDB has had nothing (notFoundTTL): most of a library is never
// added, and re-asking it weekly spent more than a key's allowance.
const (
	FoundTTL    = 30 * 24 * time.Hour
	NotFoundTTL = 7 * 24 * time.Hour
	ErrorTTL    = 24 * time.Hour

	missingLong      = 90 * 24 * time.Hour
	missingLongTTL   = 90 * 24 * time.Hour
	missingRepeatTTL = 30 * 24 * time.Hour
)

// notFoundTTL is how long a NotFound stands, from how long TheIntroDB had
// had nothing when it was fetched: a week the first time, a month after,
// a quarter once missing for 90 days.
func notFoundTTL(m *catalogv1alpha1.FileMarkers) time.Duration {
	if m.NotFoundSince == nil {
		return NotFoundTTL
	}
	switch missing := m.FetchedAt.Sub(m.NotFoundSince.Time); {
	case missing >= missingLong:
		return missingLongTTL
	case missing >= NotFoundTTL:
		return missingRepeatTTL
	default:
		return NotFoundTTL
	}
}

// Due reports whether mf needs its markers fetched now and, when it does
// not, how long until it will. Only a probed movie or episode file has
// markers: the probe's duration is what identifies its release.
func Due(mf *catalogv1alpha1.MediaFile, now time.Time) (bool, time.Duration) {
	return DueAt(mf.Spec.MediaRef.Kind, mf.Status.ProbeHash, mf.Status.MediaInfo != nil, mf.Status.Markers, now)
}

// DueAt is Due over the parts of a file the loop's draft has: its kind, its
// probe hash, whether a probe describes it, and its stored markers.
func DueAt(kind commonv1.MediaKind, probeHash string, probed bool, m *catalogv1alpha1.FileMarkers, now time.Time) (bool, time.Duration) {
	switch kind {
	case commonv1.MediaKindMovie, commonv1.MediaKindEpisode:
	default:
		return false, 0
	}
	if !probed || probeHash == "" {
		return false, 0
	}
	if m == nil || m.ForProbeHash != probeHash {
		return true, 0
	}
	ttl := ErrorTTL
	switch m.Result {
	case catalogv1alpha1.MarkersFound:
		ttl = FoundTTL
	case catalogv1alpha1.MarkersNotFound:
		ttl = notFoundTTL(m)
	}
	left := m.FetchedAt.Add(ttl).Sub(now)
	if left <= 0 {
		return true, 0
	}
	return false, left
}
