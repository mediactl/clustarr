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

package lifecycle_test

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// The fixture is one Movie with one torrent grab pinned to qbt-0, in its
// steady state: the entry's dispatch seq answered by a record at the
// engine's boot, and the stage's command book knowing that seq.
var now = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

const (
	ns       = "media"
	infoHash = "0123456789abcdef0123456789abcdef01234567"
	entryID  = "heat-1995-7f3a9c2e"
	entryUID = "6a1b2c3d-0000-4000-8000-000000000001"
	seq0     = int64(1_000)
	bootID   = "boot-1"
	outPath  = "/data/torrents/Heat.1995.1080p.BluRay.x264-GRP"
)

func owner() schema.ItemRef {
	return schema.ItemRef{Kind: "Movie", Ref: schema.Ref{Namespace: ns, Name: "heat-1995", UID: "owner-uid-1"}}
}

func torrentClient() lifecycle.Client {
	return lifecycle.Client{
		Name: "qbt", UID: "dc-uid-1", Protocol: commonv1.ProtocolTorrent, Enabled: true,
		Replicas: 1, RemoveCompleted: true,
	}
}

func usenetClient() lifecycle.Client {
	return lifecycle.Client{
		Name: "sab", UID: "dc-uid-2", Protocol: commonv1.ProtocolUsenet, Enabled: true,
		Replicas: 1, RemoveCompleted: true,
	}
}

// engine is a Ready engine on a fresh record, re-attached an hour ago at
// bootID and caught up with every resync its client asked for.
func engine(client string, ordinal int32) lifecycle.Engine {
	return lifecycle.Engine{
		Name: lifecycle.EngineName(client, ordinal), Client: client, Ordinal: ordinal,
		Ready: true, Reattached: true, BootID: bootID,
		At: now.Add(-30 * time.Second), ReattachedAt: now.Add(-time.Hour),
	}
}

func entry(phase commonv1.DownloadPhase) catalogv1alpha1.DownloadEntry {
	return catalogv1alpha1.DownloadEntry{
		ID:  entryID,
		UID: entryUID,
		Release: catalogv1alpha1.DownloadRelease{
			Title: "Heat.1995.1080p.BluRay.x264-GRP", GUID: "guid-1", IndexerRef: "tracker",
			InfoHash: infoHash, Protocol: commonv1.ProtocolTorrent, SizeBytes: 8 << 30,
		},
		Source:             commonv1.DownloadSource{IndexerDownload: &commonv1.IndexerDownload{IndexerRef: "tracker", GUID: "guid-1"}},
		Client:             "qbt",
		Engine:             "qbt-0",
		GrabbedBy:          commonv1.GrabSourceSearch,
		GrabbedAt:          metav1.NewTime(now.Add(-2 * time.Hour)),
		RemoveDataOnDelete: true,
		Phase:              phase,
		Dispatch: catalogv1alpha1.Dispatch{
			Seq: seq0, AnsweredSeq: seq0, DispatchedAt: metav1.NewTime(now.Add(-5 * time.Minute)), Destination: "qbt-0",
		},
	}
}

// usenetEntry is entry on the usenet client.
func usenetEntry(phase commonv1.DownloadPhase) catalogv1alpha1.DownloadEntry {
	e := entry(phase)
	e.Release.Protocol = commonv1.ProtocolUsenet
	e.Release.InfoHash = ""
	e.Client, e.Engine, e.Dispatch.Destination = "sab", "sab-0", "sab-0"
	return e
}

func claimOf(e catalogv1alpha1.DownloadEntry) schema.TransferClaim {
	return schema.TransferClaim{
		Owner: owner(), ProviderID: "tmdb:949", Entry: schema.EntryRef{ID: e.ID, UID: e.UID},
		InfoHash: e.Release.InfoHash, Indexer: e.Release.IndexerRef, GUID: e.Release.GUID, Title: e.Release.Title,
		Purpose: e.Purpose, RemoveDataOnDelete: e.RemoveDataOnDelete,
	}
}

// record is e's transfer record at seq, at the engine's boot.
func record(e catalogv1alpha1.DownloadEntry, seq int64, stage commonv1.DownloadStage) lifecycle.Transfer {
	o := owner()
	c := claimOf(e)
	progress := now.Add(-time.Minute)
	started := now.Add(-time.Hour)
	return lifecycle.Transfer{Record: schema.TransferRecord{
		RecordHeader:   schema.RecordHeader{Item: &o, Seq: seq, State: schema.TransferStatePresent},
		Entry:          schema.EntryRef{ID: e.ID, UID: e.UID},
		Claim:          &c,
		Claimed:        true,
		Engine:         e.Engine,
		BootID:         bootID,
		DownloadID:     infoHash,
		Stage:          stage,
		OutputPath:     outPath,
		StartedAt:      &started,
		AddedAt:        now.Add(-2 * time.Hour),
		LastProgressAt: &progress,
	}, Revision: 7}
}

// view is the owner holding entries, each with its transfer, in its steady
// state: the book knows every stored seq.
func view(entries []catalogv1alpha1.DownloadEntry, transfers ...lifecycle.Transfer) lifecycle.View {
	v := lifecycle.View{
		Owner: owner(), ProviderID: "tmdb:949", ProviderVerified: true, Kind: commonv1.MediaKindMovie,
		Now: now, Monitored: true, EntryCap: 8, Stored: entries,
		Transfers: map[types.UID]lifecycle.Transfer{},
		Clients:   []lifecycle.Client{torrentClient(), usenetClient()},
		Engines: map[string]lifecycle.Engine{
			"qbt-0": engine("qbt", 0), "sab-0": engine("sab", 0),
		},
		Files:     map[string]int{},
		Removed:   map[string]bool{},
		Searched:  map[schema.ItemRef]time.Time{},
		Intents:   lifecycle.ParseIntents(nil),
		Issued:    map[types.UID]lifecycle.IssuedCommand{},
		Unblocked: map[string]bool{},
	}
	for _, t := range transfers {
		v.Transfers[types.UID(t.Record.Entry.UID)] = t
	}
	steady(&v)
	return v
}

// steady makes the book know every stored entry's current desired state.
func steady(v *lifecycle.View) {
	v.Issued = map[types.UID]lifecycle.IssuedCommand{}
	for i := range v.Stored {
		v.Issued[types.UID(v.Stored[i].UID)] = lifecycle.BookOf(*v, &v.Stored[i])
	}
}

// absents are the plan's absent commands.
func absents(p lifecycle.Plan) []lifecycle.Command {
	var out []lifecycle.Command
	for _, c := range p.Commands {
		if c.Cmd.Desired == schema.EngineDesiredAbsent {
			out = append(out, c)
		}
	}
	return out
}

func only(p lifecycle.Plan) *catalogv1alpha1.DownloadEntry {
	if len(p.Entries) != 1 {
		return nil
	}
	return &p.Entries[0]
}

func reasons(p lifecycle.Plan) []string {
	var out []string
	for _, e := range p.Events {
		out = append(out, e.Reason)
	}
	return out
}

func actions(p lifecycle.Plan) []string {
	var out []string
	for _, h := range p.History {
		out = append(out, h.Action)
	}
	return out
}
