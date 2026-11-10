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

// NFO is the clustarr-nfo instantiation of pkg/records (2026-11-10 design
// §8.2): one record per Artist, Album, Author or Audiobook UID, the loop's
// request and the import agent's answer. An answer is a fact: the files
// are on disk whether or not the loop incorporated it.
func NFO() records.Spec[*schema.NFORecord] {
	return records.Spec[*schema.NFORecord]{
		Remediation: "nfo",
		Bucket:      events.BucketNFO,
		Schema:      schema.NFORecord{}.Schema(),
		MaxValue:    int(events.NFOMaxValueSize),
		New:         func() *schema.NFORecord { return &schema.NFORecord{} },
		KeyOf: func(r *schema.NFORecord) (string, string) {
			if r.Item == nil {
				return "", ""
			}
			return r.Item.UID, ""
		},
		Answered: records.TerminalAnswered,
		Fact:     func(r *schema.NFORecord) bool { return r.Answer != nil && len(r.Answer.Files) > 0 },
	}
}

// NFOKey is an item's NFO record key.
func NFOKey(itemUID string) string { return events.RecordKey(itemUID) }
