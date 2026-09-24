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
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Target is the custom resource a domain event or a dead letter concerns, or
// as much of it as [Resolve] could establish without guessing.
//
// Namespace is populated whenever anything is known at all. Name, Kind and
// APIVersion are populated together or not at all: there is no partial
// object identity, only "we know exactly which object" or "we don't".
type Target struct {
	Namespace  string
	Name       string
	Kind       string
	APIVersion string
}

// KindKnown reports whether t names one specific object unambiguously.
func (t Target) KindKnown() bool { return t.Kind != "" && t.Name != "" }

// HasNamespace reports whether at least the namespace is known, which is
// what the DLQ projector's namespace-level Event fallback needs.
func (t Target) HasNamespace() bool { return t.Namespace != "" }

// object builds the (possibly partial) unstructured representation of t,
// suitable both as the body of a server-side-apply PATCH (when KindKnown, it
// carries nothing but identity until a caller adds the one field it means to
// own) and as the "regarding" argument to an EventRecorder: reference.
// GetReference reads the GVK directly off TypeMeta without touching the
// manager's Scheme, so this works for a CRD the same way it works for a
// built-in type.
func (t Target) object() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(t.APIVersion)
	u.SetKind(t.Kind)
	u.SetNamespace(t.Namespace)
	u.SetName(t.Name)
	return u
}

// mediaKindNames maps commonv1.MediaKind to the exact catalog.clustarr.io
// Kind literal. All ten catalog media kinds are single lowercase words whose
// CRD Kind is just that word capitalised, but this is a table rather than an
// algorithm: an algorithm would silently "resolve" an eleventh kind nobody
// taught it, which is exactly the guessing this package exists to avoid.
var mediaKindNames = map[commonv1.MediaKind]string{
	commonv1.MediaKindMovie:     "Movie",
	commonv1.MediaKindSeries:    "Series",
	commonv1.MediaKindEpisode:   "Episode",
	commonv1.MediaKindArtist:    "Artist",
	commonv1.MediaKindAlbum:     "Album",
	commonv1.MediaKindAuthor:    "Author",
	commonv1.MediaKindBook:      "Book",
	commonv1.MediaKindAudiobook: "Audiobook",
	commonv1.MediaKindComic:     "Comic",
	commonv1.MediaKindIssue:     "Issue",
}

// mediaTarget resolves a commonv1.MediaRef-shaped reference: every catalog
// media kind lives in catalog.clustarr.io, so only the specific Kind varies.
// An unrecognised kind (never emitted by a real producer, since MediaKind is
// a CRD enum, but a DLQ envelope is untrusted bytes off the wire) leaves Kind
// empty, which KindKnown reports honestly rather than defaulting to
// something.
func mediaTarget(ns, name string, kind commonv1.MediaKind) Target {
	t := Target{Namespace: ns, Name: name}
	if k, ok := mediaKindNames[kind]; ok {
		t.Kind = k
		t.APIVersion = catalogv1alpha1.GroupVersion.String()
	}
	return t
}

// refTarget resolves a schema.Ref that already carries its own namespace --
// every non-media Ref in pkg/events/schema does -- against a known GVK.
func refTarget(ref schema.Ref, apiVersion, kind string) Target {
	return Target{Namespace: ref.Namespace, Name: ref.Name, APIVersion: apiVersion, Kind: kind}
}

// splitKey parses the Clustarr-Key convention, "<namespace>/<name>" (see
// events.HeaderKey), the same way app/catalog/worker/rssmatcher.Handle does.
// A key with no slash means the producer's Ref had an empty namespace (see
// schema.Ref.String), so the whole value is the name and the namespace is
// unknown -- not the other way around, which would attribute an object to a
// namespace that is actually its name.
func splitKey(key string) (ns, name string) {
	ns, name, ok := strings.Cut(key, "/")
	if !ok {
		return "", key
	}
	return ns, name
}

func namespaceOf(key string) string {
	ns, _ := splitKey(key)
	return ns
}

// resolver decodes one known payload schema and resolves the object it
// concerns. key is the envelope's Clustarr-Key; it is the ONLY source of
// namespace for the catalog media-kind payloads, whose commonv1.MediaRef
// deliberately carries no namespace of its own (it "lives in the same
// namespace as the object holding the reference" -- see media_types.go).
type resolver func(key string, data []byte) Target

// resolvers maps a Clustarr-Schema value to how to resolve the object it
// concerns. Every entry here is a payload this package has actually read the
// struct definition for in pkg/events/schema; a schema not listed here falls
// through to the "cannot resolve" path in both Sink and DLQProjector rather
// than being guessed at.
//
// catalog.WantedScan names no single object -- it is a namespace sweep,
// Namespace and no Name -- so resolveWantedScan resolves it to its namespace
// alone rather than pretending it names one object. A payload with no
// namespace either, a cluster-global task, would fall through with an empty
// key and resolve to nothing; no schema publishes one today (the
// index.DefinitionsSync this comment once cited never had a producer and was
// removed in gap fixes Z2).
var resolvers = map[string]resolver{
	schema.ItemEvent{}.Schema():         resolveItemEvent,
	schema.ReleaseEvent{}.Schema():      resolveReleaseEvent,
	schema.MediaFileEvent{}.Schema():    resolveMediaFileEvent,
	schema.ImportListSynced{}.Schema():  resolveImportListSynced,
	schema.SearchTask{}.Schema():        resolveSearchTask,
	schema.GrabTask{}.Schema():          resolveGrabTask,
	schema.MetadataTask{}.Schema():      resolveMetadataTask,
	schema.WantedScan{}.Schema():        resolveWantedScan,
	schema.ImportTask{}.Schema():        resolveImportTask,
	schema.Release{}.Schema():           resolveRelease,
	schema.IndexerEvent{}.Schema():      resolveIndexerEvent,
	schema.RssTask{}.Schema():           resolveRssTask,
	schema.DownloadEvent{}.Schema():     resolveDownloadEvent,
	schema.JobEvent{}.Schema():          resolveJobEvent,
	transcodeTaskRef{}.Schema():         resolveTranscodeTask,
	schema.SubtitleEvent{}.Schema():     resolveSubtitleEvent,
	schema.FetchTask{}.Schema():         resolveFetchTask,
	schema.ScanTask{}.Schema():          resolveScanTask,
	schema.ListTask{}.Schema():          resolveListTask,
	schema.ArtworkFetchTask{}.Schema():  resolveArtworkFetchTask,
	schema.RenderOverlayTask{}.Schema(): resolveRenderOverlayTask,
}

// Resolve establishes the CR a domain event or dead-lettered envelope
// concerns, or as much of that as the envelope actually supports -- see
// Target's doc comment. It never returns an error: a payload this package
// does not recognise, or cannot decode, is not a bug in the caller, it is
// exactly the "cannot resolve unambiguously" case both Sink and DLQProjector
// are required to fall back from rather than guess through.
func Resolve(env *events.Envelope) Target {
	if env == nil {
		return Target{}
	}
	r, ok := resolvers[env.Schema]
	if !ok {
		return Target{Namespace: namespaceOf(env.Key)}
	}
	return r(env.Key, env.Data)
}

func resolveItemEvent(key string, data []byte) Target {
	var p schema.ItemEvent
	if err := schema.Decode(p.Schema(), data, &p); err != nil {
		return Target{Namespace: namespaceOf(key)}
	}
	ns, name := p.Ref.Namespace, p.Ref.Name
	if name == "" {
		ns, name = splitKey(key)
	}
	return mediaTarget(ns, name, p.Media.Kind)
}

func resolveReleaseEvent(key string, data []byte) Target {
	var p schema.ReleaseEvent
	if err := schema.Decode(p.Schema(), data, &p); err != nil {
		return Target{Namespace: namespaceOf(key)}
	}
	return mediaTarget(namespaceOf(key), p.Media.Name, p.Media.Kind)
}

func resolveMediaFileEvent(key string, data []byte) Target {
	var p schema.MediaFileEvent
	if err := schema.Decode(p.Schema(), data, &p); err != nil {
		return Target{Namespace: namespaceOf(key)}
	}
	return mediaTarget(namespaceOf(key), p.Media.Name, p.Media.Kind)
}

func resolveImportListSynced(key string, data []byte) Target {
	var p schema.ImportListSynced
	if err := schema.Decode(p.Schema(), data, &p); err != nil {
		return Target{Namespace: namespaceOf(key)}
	}
	return refTarget(p.ListRef, catalogv1alpha1.GroupVersion.String(), "ImportList")
}

func resolveSearchTask(key string, data []byte) Target {
	var p schema.SearchTask
	if err := schema.Decode(p.Schema(), data, &p); err != nil {
		return Target{Namespace: namespaceOf(key)}
	}
	return mediaTarget(namespaceOf(key), p.MediaRef.Name, p.MediaRef.Kind)
}

func resolveGrabTask(key string, data []byte) Target {
	var p schema.GrabTask
	if err := schema.Decode(p.Schema(), data, &p); err != nil {
		return Target{Namespace: namespaceOf(key)}
	}
	return mediaTarget(namespaceOf(key), p.MediaRef.Name, p.MediaRef.Kind)
}

// resolveArtworkFetchTask and resolveRenderOverlayTask resolve M7's two
// artwork work payloads (catalogarr-artwork-fetch, catalogarr-artwork-render)
// the way every other MediaRef task resolves: the item's own name and kind,
// namespaced by the envelope key.
func resolveArtworkFetchTask(key string, data []byte) Target {
	var p schema.ArtworkFetchTask
	if err := schema.Decode(p.Schema(), data, &p); err != nil {
		return Target{Namespace: namespaceOf(key)}
	}
	return mediaTarget(namespaceOf(key), p.MediaRef.Name, p.MediaRef.Kind)
}

func resolveRenderOverlayTask(key string, data []byte) Target {
	var p schema.RenderOverlayTask
	if err := schema.Decode(p.Schema(), data, &p); err != nil {
		return Target{Namespace: namespaceOf(key)}
	}
	return mediaTarget(namespaceOf(key), p.MediaRef.Name, p.MediaRef.Kind)
}

func resolveMetadataTask(key string, data []byte) Target {
	var p schema.MetadataTask
	if err := schema.Decode(p.Schema(), data, &p); err != nil {
		return Target{Namespace: namespaceOf(key)}
	}
	return mediaTarget(namespaceOf(key), p.MediaRef.Name, p.MediaRef.Kind)
}

// resolveWantedScan deliberately never sets Name/Kind: a wanted-scan sweep
// names a namespace, not one object, so KindKnown must report false for it.
func resolveWantedScan(key string, data []byte) Target {
	var p schema.WantedScan
	if err := schema.Decode(p.Schema(), data, &p); err != nil {
		return Target{Namespace: namespaceOf(key)}
	}
	return Target{Namespace: p.Namespace}
}

func resolveImportTask(key string, data []byte) Target {
	var p schema.ImportTask
	if err := schema.Decode(p.Schema(), data, &p); err != nil {
		return Target{Namespace: namespaceOf(key)}
	}
	return refTarget(p.DownloadRef, downloadv1alpha1.GroupVersion.String(), "Download")
}

// resolveRelease is indexarr's parsed-release payload. It carries no Ref of
// its own; the producer convention (see app/catalog/worker/rssmatcher.Handle)
// is Clustarr-Key = "<namespace>/<indexerName>", so the key alone resolves
// it. Info.IndexerRef is consulted only to prefer a name the payload itself
// vouches for when it disagrees with the key -- it should never disagree in
// practice, since both come from the same publish call.
func resolveRelease(key string, data []byte) Target {
	ns, name := splitKey(key)
	var p schema.Release
	if err := schema.Decode(p.Schema(), data, &p); err == nil && p.Info.IndexerRef != "" {
		name = p.Info.IndexerRef
	}
	if name == "" {
		return Target{Namespace: ns}
	}
	return Target{Namespace: ns, Name: name, APIVersion: indexv1alpha1.GroupVersion.String(), Kind: "Indexer"}
}

func resolveIndexerEvent(key string, data []byte) Target {
	var p schema.IndexerEvent
	if err := schema.Decode(p.Schema(), data, &p); err != nil {
		return Target{Namespace: namespaceOf(key)}
	}
	return refTarget(p.IndexerRef, indexv1alpha1.GroupVersion.String(), "Indexer")
}

func resolveRssTask(key string, data []byte) Target {
	var p schema.RssTask
	if err := schema.Decode(p.Schema(), data, &p); err != nil {
		return Target{Namespace: namespaceOf(key)}
	}
	return refTarget(p.IndexerRef, indexv1alpha1.GroupVersion.String(), "Indexer")
}

func resolveDownloadEvent(key string, data []byte) Target {
	var p schema.DownloadEvent
	if err := schema.Decode(p.Schema(), data, &p); err != nil {
		return Target{Namespace: namespaceOf(key)}
	}
	return refTarget(p.DownloadRef, downloadv1alpha1.GroupVersion.String(), "Download")
}

func resolveJobEvent(key string, data []byte) Target {
	var p schema.JobEvent
	if err := schema.Decode(p.Schema(), data, &p); err != nil {
		return Target{Namespace: namespaceOf(key)}
	}
	return refTarget(p.JobRef, transcodev1alpha1.GroupVersion.String(), "TranscodeJob")
}

// transcodeTaskRef is the one field of app/squash/task.Task a dead letter
// needs: the TranscodeJob the task was dispatched for. It is decoded here
// rather than importing app/squash/task, which would make catalogarr depend
// on squasharr's worker types for one reference.
type transcodeTaskRef struct {
	Job schema.Ref `json:"job"`
}

// Schema implements schema.Payload; it is app/squash/task.Task's.
func (transcodeTaskRef) Schema() string { return "transcode.Task.v1" }

// resolveTranscodeTask names the TranscodeJob a dead-lettered transcode task
// was dispatched for, so the DLQ projector annotates it and squasharr blocks
// the job (spec §18.3).
func resolveTranscodeTask(key string, data []byte) Target {
	var p transcodeTaskRef
	if err := schema.Decode(p.Schema(), data, &p); err != nil || p.Job.Name == "" {
		return Target{Namespace: namespaceOf(key)}
	}
	return refTarget(p.Job, transcodev1alpha1.GroupVersion.String(), "TranscodeJob")
}

func resolveSubtitleEvent(key string, data []byte) Target {
	var p schema.SubtitleEvent
	if err := schema.Decode(p.Schema(), data, &p); err != nil {
		return Target{Namespace: namespaceOf(key)}
	}
	return refTarget(p.RequestRef, subtitlev1alpha1.GroupVersion.String(), "SubtitleRequest")
}

func resolveFetchTask(key string, data []byte) Target {
	var p schema.FetchTask
	if err := schema.Decode(p.Schema(), data, &p); err != nil {
		return Target{Namespace: namespaceOf(key)}
	}
	return refTarget(p.RequestRef, subtitlev1alpha1.GroupVersion.String(), "SubtitleRequest")
}

func resolveScanTask(key string, data []byte) Target {
	var p schema.ScanTask
	if err := schema.Decode(p.Schema(), data, &p); err != nil {
		return Target{Namespace: namespaceOf(key)}
	}
	return refTarget(p.LibraryScanRef, catalogv1alpha1.GroupVersion.String(), "LibraryScan")
}

// resolveListTask is importarr's import-list sync task. Since the pruned
// catalog.ImportListTask went (X1 item 14), this is the only task that names
// an ImportList, so without it a sync that dead-lettered reached nothing but
// a namespace Event, and the ImportList never showed DeadLettered.
func resolveListTask(key string, data []byte) Target {
	var p schema.ListTask
	if err := schema.Decode(p.Schema(), data, &p); err != nil {
		return Target{Namespace: namespaceOf(key)}
	}
	return refTarget(p.ListRef, catalogv1alpha1.GroupVersion.String(), "ImportList")
}
