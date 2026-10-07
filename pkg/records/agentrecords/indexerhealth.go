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

package agentrecords

import (
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

// IndexerHealth is clustarr-indexer-health's protocol (ADR-0019 §7.5): one
// record per Indexer, keyed events.RecordKey(indexer uid), written by the
// index agent, whose search fan-out and RSS poll share one in-process
// compare-and-swap (records.Writer.Update), and read by the Indexer
// controller.
func IndexerHealth() records.Spec[*schema.IndexerHealthRecord] {
	return records.Spec[*schema.IndexerHealthRecord]{
		Remediation: "indexerHealth",
		Bucket:      events.BucketIndexerHealth,
		Schema:      schema.IndexerHealthRecord{}.Schema(),
		MaxValue:    int(events.IndexerHealthMaxValueSize),
		New:         func() *schema.IndexerHealthRecord { return &schema.IndexerHealthRecord{} },
		KeyOf: func(r *schema.IndexerHealthRecord) (string, string) {
			if r.Item == nil {
				return "", ""
			}
			return r.Item.UID, ""
		},
	}
}

// IndexerHealthKey is an Indexer's health record key.
func IndexerHealthKey(indexerUID string) string { return events.RecordKey(indexerUID) }
