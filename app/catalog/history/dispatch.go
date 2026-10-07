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

package history

import (
	"strconv"
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// The sub of a dispatch key (app/dispatch.Key.Sub): which of a CR's
// dispatches a task is. One CR holds several -- an item's search beside each
// grab entry's engine command and import -- so the sub tells them apart.
// Planners and the advisory intake (ADR-0019 §8.2) build keys from these.
const (
	DispatchSubSearch       = "search"
	DispatchSubMetadata     = "metadata"
	DispatchSubArtworkFetch = "artwork-fetch"
	DispatchSubOverlay      = "overlay"
	DispatchSubProbe        = "probe"
	DispatchSubMarkers      = "markers"
	DispatchSubTranscode    = "transcode"
	DispatchSubSubtitle     = "subtitle"
	DispatchSubSegments     = "segments"
	DispatchSubScan         = "scan"
	DispatchSubList         = "list"
	DispatchSubRecycle      = "recycle"
	DispatchSubRSS          = "rss"
	DispatchSubImportV1     = "import"
)

// DispatchSubEntry is a grab entry's engine-command dispatch: the entry's
// UID (§6.2: entry.dispatch).
func DispatchSubEntry(entryUID string) string { return entryUID }

// DispatchSubImport is a grab entry's import dispatch (entry.import.dispatch),
// which fences the inspect and the execute task alike (§6.9):
// "<entry uid>/import".
func DispatchSubImport(entryUID string) string { return entryUID + "/import" }

// catalogKinds are the catalog.clustarr.io kinds an ItemRef may name.
var catalogKinds = map[string]bool{
	"Movie": true, "Series": true, "Episode": true, "Artist": true, "Album": true,
	"Author": true, "Book": true, "Audiobook": true, "Comic": true, "Issue": true,
	"Search": true, "ImportList": true, "LibraryScan": true, "MediaFile": true,
}

// itemTarget resolves a schema.ItemRef: an item, a Search, an ImportList, an
// Indexer or a DownloadClient, its Kind the object's. An unknown kind leaves
// Kind empty (KindKnown false), never a guess.
func itemTarget(ref schema.ItemRef) Target {
	t := Target{Namespace: ref.Namespace, Name: ref.Name}
	kind := ref.Kind
	if k, ok := mediaKindNames[commonv1.MediaKind(strings.ToLower(kind))]; ok {
		kind = k
	}
	switch {
	case catalogKinds[kind]:
		t.Kind, t.APIVersion = kind, catalogv1alpha1.GroupVersion.String()
	case kind == "Indexer":
		t.Kind, t.APIVersion = kind, indexv1alpha1.GroupVersion.String()
	case kind == "DownloadClient":
		t.Kind, t.APIVersion = kind, downloadv1alpha1.GroupVersion.String()
	}
	return t
}

// ResolveDispatch maps a task envelope to the CR whose dispatch it is, the
// dispatch seq its payload carries (0 for a kind with no status dispatch
// block: metadata, artwork fetch, overlay render (ruling R23), and the
// per-object tasks), and the dispatch's sub (the DispatchSub* conventions);
// ok is false for a payload that is no dispatched task or that names no
// object. subject is the subject the task was published on, for the payloads
// whose object only the subject names (none today).
func ResolveDispatch(subject string, env *events.Envelope) (t Target, seq int64, sub string, ok bool) {
	_ = subject
	if env == nil {
		return Target{}, 0, "", false
	}
	switch env.Schema {
	case schema.SearchTask{}.Schema(), schema.SearchTaskV1Schema:
		var p schema.SearchTask
		if err := schema.Decode(env.Schema, env.Data, &p); err != nil {
			return Target{}, 0, "", false
		}
		switch {
		case p.SearchRef != nil && p.SearchRef.Name != "":
			ns := p.SearchRef.Namespace
			if ns == "" {
				ns = namespaceOf(env.Key)
			}
			t = Target{Namespace: ns, Name: p.SearchRef.Name, Kind: "Search", APIVersion: catalogv1alpha1.GroupVersion.String()}
		case p.Item.Name != "":
			t = itemTarget(p.Item)
		default:
			t = Resolve(env)
		}
		return t, p.Seq, DispatchSubSearch, t.KindKnown()
	case schema.MetadataTask{}.Schema(), schema.MetadataTaskV1Schema:
		t = Resolve(env)
		return t, 0, DispatchSubMetadata, t.KindKnown()
	case schema.ArtworkFetchTask{}.Schema(), schema.ArtworkFetchTaskV1Schema:
		t = Resolve(env)
		return t, 0, DispatchSubArtworkFetch, t.KindKnown()
	case schema.RenderOverlayTask{}.Schema():
		t = Resolve(env)
		return t, 0, DispatchSubOverlay, t.KindKnown()
	case schema.ProbeTask{}.Schema():
		var p schema.ProbeTask
		if err := schema.Decode(env.Schema, env.Data, &p); err != nil || p.MediaFile.Name == "" {
			return Target{}, 0, "", false
		}
		return mediaFileTarget(p.MediaFile.Namespace, p.MediaFile.Name), p.Seq, DispatchSubProbe, true
	case schema.MarkersTask{}.Schema():
		var p schema.MarkersTask
		if err := schema.Decode(env.Schema, env.Data, &p); err != nil || p.File.Name == "" {
			return Target{}, 0, "", false
		}
		return mediaFileTarget(p.File.Namespace, p.File.Name), p.Seq, DispatchSubMarkers, true
	case transcodeTaskRef{}.Schema():
		var p transcodeTaskAttempt
		if err := schema.Decode(env.Schema, env.Data, &p); err != nil || p.Job.Name == "" {
			return Target{}, 0, "", false
		}
		return resolveTranscodeTask(env.Key, env.Data), int64(p.Attempt), DispatchSubTranscode, true
	case schema.FetchTask{}.Schema():
		var p schema.FetchTask
		if err := schema.Decode(env.Schema, env.Data, &p); err != nil || p.RequestRef.Name == "" {
			return Target{}, 0, "", false
		}
		return resolveFetchTask(env.Key, env.Data), 0, DispatchSubSubtitle + "/" + p.LangKey, true
	case schema.SegmentsPlanTask{}.Schema(), schema.AnalyzeTask{}.Schema():
		t = Resolve(env)
		return t, 0, DispatchSubSegments, t.KindKnown()
	case schema.ScanTask{}.Schema():
		t = Resolve(env)
		return t, 0, DispatchSubScan, t.KindKnown()
	case schema.ListTask{}.Schema():
		t = Resolve(env)
		return t, 0, DispatchSubList, t.KindKnown()
	case schema.RssTask{}.Schema():
		t = Resolve(env)
		return t, 0, DispatchSubRSS, t.KindKnown()
	case schema.ImportTask{}.Schema():
		t = Resolve(env)
		return t, 0, DispatchSubImportV1, t.KindKnown()
	case schema.EngineCommand{}.Schema():
		var p schema.EngineCommand
		if err := schema.Decode(env.Schema, env.Data, &p); err != nil || p.Owner.Name == "" || p.Entry.UID == "" {
			return Target{}, 0, "", false // a resync or an unidentified removal names no entry
		}
		t = itemTarget(p.Owner)
		return t, p.Seq, DispatchSubEntry(p.Entry.UID), t.KindKnown()
	case schema.ImportInspectTask{}.Schema():
		var p schema.ImportInspectTask
		if err := schema.Decode(env.Schema, env.Data, &p); err != nil || p.Owner.Name == "" || p.Entry.UID == "" {
			return Target{}, 0, "", false
		}
		t = itemTarget(p.Owner)
		return t, p.Seq, DispatchSubImport(p.Entry.UID), t.KindKnown()
	case schema.ImportExecuteTask{}.Schema():
		var p schema.ImportExecuteTask
		if err := schema.Decode(env.Schema, env.Data, &p); err != nil || p.Owner.Name == "" || p.Entry.UID == "" {
			return Target{}, 0, "", false
		}
		t = itemTarget(p.Owner)
		return t, p.Seq, DispatchSubImport(p.Entry.UID), t.KindKnown()
	}
	// importarr.RecycleSweepTask names no object; RecycleFilesTask names a
	// RootFolder, which holds no dispatch.
	return Target{}, 0, "", false
}

// transcodeTaskAttempt is app/transcode/task.Task's job and attempt, decoded
// without importing it (transcodeTaskRef's reason).
type transcodeTaskAttempt struct {
	Job     schema.Ref `json:"job"`
	Attempt int32      `json:"attempt"`
}

// Schema implements schema.Payload; it is app/transcode/task.Task's.
func (transcodeTaskAttempt) Schema() string { return transcodeTaskRef{}.Schema() }

// DispatchID is what a task's Msg-Id says of its dispatch when the message
// itself is gone (a term on a WorkQueue stream, ruling R10): the UID of the
// object the task is keyed by, the dispatch seq and sub, and the kind the
// UID names when the shape says.
type DispatchID struct {
	// UID is the task's key: an item's or a Search's (search), an entry's
	// (engine, import), a MediaFile's (probe, markers) or a TranscodeJob's.
	UID string
	Seq int64
	Sub string
	// Kind is "MediaFile" or "TranscodeJob" when the shape names it; ""
	// for an item, a Search or an entry.
	Kind string
	// Entry marks a UID that is a grab entry's, found through its owner's
	// status.downloads.
	Entry bool
}

// ParseDispatchID reads the Msg-Id shapes of the dispatched tasks
// (pkg/events: MsgIDForSearch, MsgIDForEngineCommand[AfterBoot],
// MsgIDForImport, MsgIDForProbe, MsgIDForMarkers[At],
// MsgIDForTranscodeTask). A resync's, an unidentified removal's and every
// other shape is not ok.
func ParseDispatchID(id string) (DispatchID, bool) {
	parts := strings.Split(id, "/")
	num := func(s string) (int64, bool) {
		n, err := strconv.ParseInt(s, 10, 64)
		return n, err == nil && n >= 0
	}
	switch {
	case len(parts) == 3 && parts[0] == "search":
		if n, ok := num(parts[2]); ok && parts[1] != "" {
			return DispatchID{UID: parts[1], Seq: n, Sub: DispatchSubSearch}, true
		}
	case (len(parts) == 3 || len(parts) == 4) && parts[0] == "engine":
		if n, ok := num(parts[2]); ok && parts[1] != "" {
			return DispatchID{UID: parts[1], Seq: n, Sub: DispatchSubEntry(parts[1]), Entry: true}, true
		}
	case len(parts) == 4 && parts[0] == "import":
		if n, ok := num(parts[3]); ok && parts[1] != "" &&
			(parts[2] == schema.ImportSubInspect || parts[2] == schema.ImportSubExecute) {
			return DispatchID{UID: parts[1], Seq: n, Sub: DispatchSubImport(parts[1]), Entry: true}, true
		}
	case len(parts) == 5 && parts[0] == "probe":
		if n, ok := num(parts[4]); ok && parts[1] != "" {
			return DispatchID{UID: parts[1], Seq: n, Sub: DispatchSubProbe, Kind: "MediaFile"}, true
		}
	case (len(parts) == 3 || len(parts) == 4) && parts[0] == "markers":
		if n, ok := num(parts[2]); ok && parts[1] != "" {
			return DispatchID{UID: parts[1], Seq: n, Sub: DispatchSubMarkers, Kind: "MediaFile"}, true
		}
	case len(parts) == 2:
		if n, ok := num(parts[1]); ok && parts[0] != "" {
			return DispatchID{UID: parts[0], Seq: n, Sub: DispatchSubTranscode, Kind: "TranscodeJob"}, true
		}
	}
	return DispatchID{}, false
}

// resolveEngineCommand, resolveImportInspect, resolveImportExecute,
// resolveCandidate and resolveScanObservation resolve ADR-0019's task and
// intake payloads to their owner (the DLQ projector's annotation target).
func resolveEngineCommand(key string, data []byte) Target {
	var p schema.EngineCommand
	if err := schema.Decode(p.Schema(), data, &p); err != nil || p.Owner.Name == "" {
		return Target{Namespace: namespaceOf(key)}
	}
	return itemTarget(p.Owner)
}

func resolveImportInspect(key string, data []byte) Target {
	var p schema.ImportInspectTask
	if err := schema.Decode(p.Schema(), data, &p); err != nil || p.Owner.Name == "" {
		return Target{Namespace: namespaceOf(key)}
	}
	return itemTarget(p.Owner)
}

func resolveImportExecute(key string, data []byte) Target {
	var p schema.ImportExecuteTask
	if err := schema.Decode(p.Schema(), data, &p); err != nil || p.Owner.Name == "" {
		return Target{Namespace: namespaceOf(key)}
	}
	return itemTarget(p.Owner)
}

func resolveCandidate(key string, data []byte) Target {
	var p schema.Candidate
	if err := schema.Decode(p.Schema(), data, &p); err != nil || p.Owner.Name == "" {
		return Target{Namespace: namespaceOf(key)}
	}
	return itemTarget(p.Owner)
}

func resolveScanObservation(key string, data []byte) Target {
	var p schema.ScanObservation
	if err := schema.Decode(p.Schema(), data, &p); err != nil || p.Scan.Name == "" {
		return Target{Namespace: namespaceOf(key)}
	}
	return refTarget(p.Scan, catalogv1alpha1.GroupVersion.String(), "LibraryScan")
}
