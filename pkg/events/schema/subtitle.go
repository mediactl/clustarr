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

import "time"

// SubtitleEvent reports a subtitle being downloaded, upgraded or failing.
// Subject:
// clustarr.evt.subtitle.subtitle.<downloaded|upgraded|failed>.<request-uid>.
type SubtitleEvent struct {
	// RequestRef is the SubtitleRequest the event is about.
	RequestRef Ref `json:"requestRef"`

	// Action is one of downloaded, upgraded or failed.
	Action string `json:"action"`

	// LangKey is the language key, e.g. "en", "en:forced" or "en:hi".
	LangKey string `json:"langKey"`

	// Provider is the subtitle provider that served the file.
	Provider string `json:"provider,omitempty"`

	// ProviderID is the provider-scoped subtitle identifier.
	ProviderID string `json:"providerID,omitempty"`

	// Score is the match score awarded to the chosen candidate.
	Score int32 `json:"score,omitempty"`

	// PreviousScore is the score of the subtitle that was replaced, for an
	// upgrade.
	PreviousScore int32 `json:"previousScore,omitempty"`

	// Path is where the sidecar was written.
	Path string `json:"path,omitempty"`

	// Reason explains a failure.
	Reason string `json:"reason,omitempty"`

	// At is when the transition happened.
	At time.Time `json:"at"`
}

// Schema implements Payload.
func (SubtitleEvent) Schema() string { return "subtitle.SubtitleEvent.v1" }

// FetchTask asks a fetch worker to look for one language of one subtitle
// request. Subject:
// clustarr.work.captionarr.fetch.<priority>.<request-uid>.<langKey>.
type FetchTask struct {
	// RequestRef is the SubtitleRequest to satisfy.
	RequestRef Ref `json:"requestRef"`

	// LangKey is the language key to fetch, e.g. "en" or "en:forced".
	LangKey string `json:"langKey"`

	// MinScore is the lowest acceptable match score.
	MinScore int32 `json:"minScore,omitempty"`

	// Upgrade says an acceptable subtitle already exists and only a better
	// one should be taken.
	Upgrade bool `json:"upgrade,omitempty"`

	// ProbeHash identifies the exact media file the request was planned
	// against. It is part of the deduplication ID, so a re-probe that yields
	// the same hash does not re-enqueue the fetch.
	ProbeHash string `json:"probeHash,omitempty"`
}

// Schema implements Payload.
func (FetchTask) Schema() string { return "subtitle.FetchTask.v1" }
