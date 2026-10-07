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

// ItemMetadata is clustarr-item-metadata's protocol (ADR-0019 §7.1): one
// record per catalog item, keyed events.RecordKey(item uid), written by the
// metadata gateway and its artwork fetcher (one process, one
// records.Writer), read by the item keys and the Artist and Author
// reconcilers.
func ItemMetadata() records.Spec[*schema.ItemMetadataRecord] {
	return records.Spec[*schema.ItemMetadataRecord]{
		Remediation: "metadata",
		Bucket:      events.BucketItemMetadata,
		Schema:      schema.ItemMetadataRecord{}.Schema(),
		MaxValue:    int(events.ItemMetadataMaxValueSize),
		New:         func() *schema.ItemMetadataRecord { return &schema.ItemMetadataRecord{} },
		KeyOf: func(r *schema.ItemMetadataRecord) (string, string) {
			if r.Item == nil {
				return "", ""
			}
			return r.Item.UID, ""
		},
	}
}

// ItemMetadataKey is an item's metadata record key.
func ItemMetadataKey(itemUID string) string { return events.RecordKey(itemUID) }
