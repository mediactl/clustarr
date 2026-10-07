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

package metadata

import (
	"context"
	"math"

	"github.com/mediactl/clustarr/app/catalog/controller/itempass"
	"github.com/mediactl/clustarr/app/catalog/metadatainputs"
	"github.com/mediactl/clustarr/app/catalog/metadataset"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// Options is what the metadata stage reads.
type Options struct {
	Bus events.Bus
	// Artwork renders status.artwork from the artwork objects' metadata
	// (A5.2, catalogartwork.Entries over the manager's index); nil
	// re-declares the stored entries.
	Artwork metadataset.EntriesFunc
}

// Stage is the metadata item stage (remediation.ItemStage and Watcher).
type Stage struct {
	o       Options
	records *records.Reader[*schema.ItemMetadataRecord]
}

var (
	_ remediation.ItemStage = (*Stage)(nil)
	_ remediation.Watcher   = (*Stage)(nil)
)

// New builds the metadata stage.
func New(o Options) *Stage {
	return &Stage{o: o, records: records.NewReader(o.Bus.KV(events.BucketItemMetadata), agentrecords.ItemMetadata())}
}

// Name implements remediation.ItemStage.
func (s *Stage) Name() remediation.ItemStageName { return remediation.StageMetadata }

// Applies implements remediation.ItemStage: the six loop kinds with
// metadata of their own.
func (s *Stage) Applies(k remediation.KeyKind) bool { return applies(k) }

func applies(k remediation.KeyKind) bool {
	switch k {
	case remediation.KindMovie, remediation.KindSeries, remediation.KindAlbum, remediation.KindBook,
		remediation.KindAudiobook, remediation.KindComic:
		return true
	}
	return false
}

// Plan implements remediation.ItemStage: read the item's record; when it
// answers the item's current ask with a newer document (§7.1 "Ordering"),
// incorporate it; render the set when it or status.artwork moved; answer
// the ledger on an incorporation.
func (s *Stage) Plan(ctx context.Context, _ *remediation.Env, v *remediation.ItemView) (remediation.ItemResult, error) {
	uid := string(v.Item.GetUID())
	rec, _, ok, err := s.records.Get(ctx, agentrecords.ItemMetadataKey(uid))
	if err != nil {
		return remediation.ItemResult{}, remediation.Transient(err)
	}
	in := metadataset.Input{Item: v.Item}
	if ok && metadatainputs.Incorporable(v.Item, rec) {
		in.Record = rec
	}
	if s.o.Artwork != nil {
		in.Entries, in.EntriesKnown = s.o.Artwork(v.Item)
	}
	var res remediation.ItemResult
	if set, owed := metadataset.Build(in); owed {
		res.Sets = []itempass.Set{set}
	}
	if in.Record != nil {
		// Every slot of the item's metadata dispatch goes (R23): the answer
		// landed.
		res.Effects = append(res.Effects, remediation.DispatchAnswered{
			Durable: metadatainputs.Durable, Ledger: remediation.ItemLedgerKey(v.Key), Seq: math.MaxInt64,
		})
	}
	return res, nil
}
