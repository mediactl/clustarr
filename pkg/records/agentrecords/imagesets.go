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

// ImageSets is clustarr-image-sets' protocol (spec 2026-10-22): one record
// per Movie or Series, keyed events.RecordKey(item uid). The remediation
// loop's image-set stage writes the request, the metadata agent's image-set
// worker answers by compare-and-swap, and the stage incorporates the answer
// into status.imageSet. No answer is a fact: nothing on disk changes.
func ImageSets() records.Spec[*schema.ImageSetRecord] {
	return records.Spec[*schema.ImageSetRecord]{
		Remediation: "imageset",
		Bucket:      events.BucketImageSets,
		Schema:      schema.ImageSetRecord{}.Schema(),
		MaxValue:    int(events.ImageSetsMaxValueSize),
		New:         func() *schema.ImageSetRecord { return &schema.ImageSetRecord{} },
		KeyOf: func(r *schema.ImageSetRecord) (string, string) {
			if r.Item == nil {
				return "", ""
			}
			return r.Item.UID, ""
		},
		Answered: records.TerminalAnswered,
		Fact:     func(*schema.ImageSetRecord) bool { return false },
	}
}

// ImageSetKey is an item's image-set record key.
func ImageSetKey(itemUID string) string { return events.RecordKey(itemUID) }
