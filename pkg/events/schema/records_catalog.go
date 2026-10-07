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
	"encoding/json"
	"time"
)

// Bounds of the catalog agents' records (ADR-0019 §7.1, §7.4). The writers
// clamp to them (A4.3, A5.1).
const (
	// MaxSearchCandidates caps SearchRecord.Candidates.
	MaxSearchCandidates = 200
	// MaxIndexerOutcomes caps SearchRecord.IndexerOutcomes.
	MaxIndexerOutcomes = 100
	// MaxArtworkFailures caps ItemMetadataRecord.ArtworkFailures: one per
	// artwork type.
	MaxArtworkFailures = 9
)

// SearchRecord is clustarr-searches' value for one search task, keyed
// events.RecordKey(task uid): the Search's UID for an interactive search,
// the item's otherwise (ADR-0019 §7.4). Written only by the search agent:
// Item is the item, or the Search for an interactive search; Seq the task's.
// The Search controller incorporates an interactive one; the item key's
// grab planner reads an automatic one while it answers the item's current
// searchDispatch and is younger than grabplan.CandidateWindow (ruling R7).
type SearchRecord struct {
	RecordHeader
	// Task is the task uid the key is built from.
	Task string `json:"task"`
	// Container is the Series or Comic of an Episode or Issue search, whose
	// grab planner reads the record too.
	Container *ItemRef `json:"container,omitempty"`
	// Purpose is SearchPurposeAudioDonor for a donor search.
	Purpose    string          `json:"purpose,omitempty"`
	FinishedAt time.Time       `json:"finishedAt"`
	QueryMode  SearchQueryMode `json:"queryMode,omitempty"`
	// AllPaced is true when every indexer was paced: not a search attempt.
	AllPaced bool `json:"allPaced,omitempty"`
	// IndexerOutcomes has at most MaxIndexerOutcomes entries.
	IndexerOutcomes []SearchOutcome `json:"indexerOutcomes,omitempty"`
	// Candidates are the ranked releases, at most MaxSearchCandidates.
	Candidates []ScoredRelease `json:"candidates,omitempty"`
}

// Schema implements Payload and is RecordHeader.Schema on every value.
func (SearchRecord) Schema() string { return "catalog.Search.v1" }

// MetadataInputs are what a metadata refresh was asked for (ADR-0019 §7.1):
// a record answers a task only when its inputs equal the item's last ask.
type MetadataInputs struct {
	ProviderIDs   map[string]string `json:"providerIDs,omitempty"`
	Language      string            `json:"language,omitempty"`
	SchemaVersion int32             `json:"schemaVersion,omitempty"`
	Force         bool              `json:"force,omitempty"`
	RefreshEpoch  int64             `json:"refreshEpoch,omitempty"`
}

// ArtworkFailure is one artwork type the gateway's fetcher could not store.
type ArtworkFailure struct {
	Type  string    `json:"type"`
	URL   string    `json:"url,omitempty"`
	Error string    `json:"error"`
	At    time.Time `json:"at"`
}

// ItemMetadataRecord is clustarr-item-metadata's value for one item, keyed
// events.RecordKey(item uid), written only by the metadata gateway (and its
// artwork fetcher, in the same process) (ADR-0019 §7.1): Item is the item,
// Seq a per-item counter. Metadata is the kind's status.metadata exactly as
// metadata/patch.go renders it, decoded by the kind's renderer.
type ItemMetadataRecord struct {
	RecordHeader
	Inputs        MetadataInputs  `json:"inputs"`
	RefreshedAt   time.Time       `json:"refreshedAt"`
	SchemaVersion int32           `json:"schemaVersion,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	// ArtworkFailures has at most MaxArtworkFailures entries.
	ArtworkFailures []ArtworkFailure `json:"artworkFailures,omitempty"`
}

// Schema implements Payload and is RecordHeader.Schema on every value.
func (ItemMetadataRecord) Schema() string { return "catalog.ItemMetadata.v1" }
