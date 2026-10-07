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

// Package agentrecords is the one declaration of each ADR-0019
// agent-written records bucket's protocol (ruling R6): its bucket, schema,
// value cap and key, shared by the agent that writes it (records.Writer)
// and the manager that reads it (records.Reader), so the two can never
// disagree on a key.
package agentrecords

import (
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

// Transfers is clustarr-transfers' protocol: one record per grab entry,
// keyed events.RecordKey(entry uid), written by the engine holding it.
func Transfers() records.Spec[*schema.TransferRecord] {
	return records.Spec[*schema.TransferRecord]{
		Remediation: "transfer",
		Bucket:      events.BucketTransfers,
		Schema:      schema.TransferRecord{}.Schema(),
		MaxValue:    int(events.TransfersMaxValueSize),
		New:         func() *schema.TransferRecord { return &schema.TransferRecord{} },
		KeyOf:       func(r *schema.TransferRecord) (string, string) { return r.Entry.UID, "" },
	}
}

// Engines is clustarr-engines' protocol: one record per DownloadClient UID
// and ordinal, keyed events.RecordSubKey(client uid, "<ordinal>").
func Engines() records.Spec[*schema.EngineRecord] {
	return records.Spec[*schema.EngineRecord]{
		Remediation: "engine",
		Bucket:      events.BucketEngines,
		Schema:      schema.EngineRecord{}.Schema(),
		MaxValue:    int(events.EnginesMaxValueSize),
		New:         func() *schema.EngineRecord { return &schema.EngineRecord{} },
		KeyOf: func(r *schema.EngineRecord) (string, string) {
			if r.Item == nil {
				return "", r.Sub
			}
			return r.Item.UID, r.Sub
		},
	}
}

// Imports is clustarr-imports' protocol: an inspect and an execute record
// per grab entry, keyed events.RecordSubKey(entry uid, phase).
func Imports() records.Spec[*schema.ImportRecord] {
	return records.Spec[*schema.ImportRecord]{
		Remediation: "import",
		Bucket:      events.BucketImports,
		Schema:      schema.ImportRecord{}.Schema(),
		MaxValue:    int(events.ImportsMaxValueSize),
		New:         func() *schema.ImportRecord { return &schema.ImportRecord{} },
		KeyOf:       func(r *schema.ImportRecord) (string, string) { return r.Entry.UID, r.Sub },
	}
}

// TransferKey is an entry's transfer record key.
func TransferKey(entryUID string) string { return events.RecordKey(entryUID) }

// EngineKey is an engine instance's record key.
func EngineKey(clientUID string, ordinal int32) string {
	return events.RecordSubKey(clientUID, itoa(ordinal))
}

// ImportKey is an entry's import record key for phase
// (schema.ImportSubInspect or ImportSubExecute).
func ImportKey(entryUID, phase string) string { return events.RecordSubKey(entryUID, phase) }

func itoa(n int32) string {
	if n == 0 {
		return "0"
	}
	var b [11]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
