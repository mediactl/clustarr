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

package engine

import (
	"context"
	"encoding/json"
	"time"

	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Journal is what an engine remembers per transfer beyond the payload
// (§6.7): the torrent descriptor and the usenet manifest both carry it.
type Journal struct {
	// EntryUID is "" for a pre-journal transfer (release N's first boot),
	// which holds only its Download name until a command names it.
	EntryUID      string                `json:"entryUID,omitempty"`
	Seq           int64                 `json:"seq,omitempty"`
	Claim         *schema.TransferClaim `json:"claim,omitempty"`
	Imported      bool                  `json:"imported,omitempty"`
	ImportedAt    *time.Time            `json:"importedAt,omitempty"`
	FailureReason string                `json:"failureReason,omitempty"`
	// Desired is the last applied: present or absent.
	Desired string `json:"desired,omitempty"`
	// HealthOverride is the last resume nonce applied to a health hold.
	HealthOverride string `json:"healthOverride,omitempty"`
}

// Transfers is what a command handler drives: one engine's client and
// journal.
type Transfers interface {
	// List is every transfer with its journal and client item.
	List(ctx context.Context) ([]Held, error)
	// Apply adds, updates or removes a transfer to match cmd; the returned
	// Held is the transfer afterwards (Found false once removed).
	Apply(ctx context.Context, cmd schema.EngineCommand, cur *Held) (Held, error)
	// RemoveByID removes an unidentified transfer by its client id.
	RemoveByID(ctx context.Context, downloadID string, removeData bool) error
}

// Held is one transfer an engine holds.
type Held struct {
	// Name is the Download name (pre-journal) or the entry id.
	Name    string
	Journal Journal
	Item    download.Item
	Found   bool
}

// Identified reports a transfer whose journal names its entry.
func (h Held) Identified() bool { return h.Journal.EntryUID != "" && h.Journal.Claim != nil }

// MarshalJournal encodes a journal for a Journaled client.
func MarshalJournal(j Journal) ([]byte, error) { return json.Marshal(j) }

// UnmarshalJournal decodes a stored journal; nil or undecodable is the zero
// journal (a pre-journal transfer).
func UnmarshalJournal(b []byte) Journal {
	var j Journal
	if len(b) > 0 {
		_ = json.Unmarshal(b, &j)
	}
	return j
}
