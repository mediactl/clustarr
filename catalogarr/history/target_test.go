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

package history_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/history"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// envelopeFor encodes p and wraps it in an Envelope with key as Clustarr-Key,
// mirroring what a real producer sends (see e.g. catalogarr/worker/grab/
// perform.go's publishGrabbed).
func envelopeFor(t *testing.T, key string, p schema.Payload) *events.Envelope {
	t.Helper()
	schemaName, data, err := schema.Encode(p)
	require.NoError(t, err)
	return &events.Envelope{Schema: schemaName, Key: key, Data: data}
}

func TestResolve_CatalogMediaKinds(t *testing.T) {
	catalogGV := "catalog.clustarr.io/v1alpha1"

	t.Run("ItemEvent prefers its own Ref over the key", func(t *testing.T) {
		env := envelopeFor(t, "wrong-ns/wrong-name", schema.ItemEvent{
			Ref:   schema.Ref{Namespace: "default", Name: "the-matrix"},
			Media: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		})
		got := history.Resolve(env)
		require.True(t, got.KindKnown())
		require.Equal(t, history.Target{
			Namespace: "default", Name: "the-matrix", Kind: "Movie", APIVersion: catalogGV,
		}, got)
	})

	t.Run("ReleaseEvent has no Ref, so namespace comes from the key", func(t *testing.T) {
		env := envelopeFor(t, "default/the-wire", schema.ReleaseEvent{
			Media:  commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: "the-wire"},
			Action: events.ActionGrabbed,
		})
		got := history.Resolve(env)
		require.Equal(t, history.Target{
			Namespace: "default", Name: "the-wire", Kind: "Series", APIVersion: catalogGV,
		}, got)
	})

	t.Run("MediaFileEvent resolves the parent catalog item", func(t *testing.T) {
		env := envelopeFor(t, "default/the-wire-s01e01", schema.MediaFileEvent{
			Media:  commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "the-wire-s01e01"},
			Action: "imported",
		})
		got := history.Resolve(env)
		require.Equal(t, "Episode", got.Kind)
		require.Equal(t, "default", got.Namespace)
	})

	t.Run("SearchTask resolves a non-video kind the same way", func(t *testing.T) {
		env := envelopeFor(t, "default/tolkien", schema.SearchTask{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAuthor, Name: "tolkien"},
			Reason:   schema.SearchReasonMissing,
		})
		got := history.Resolve(env)
		require.Equal(t, history.Target{
			Namespace: "default", Name: "tolkien", Kind: "Author", APIVersion: catalogGV,
		}, got)
	})

	t.Run("GrabTask", func(t *testing.T) {
		env := envelopeFor(t, "default/dune", schema.GrabTask{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "dune"},
		})
		got := history.Resolve(env)
		require.Equal(t, "Book", got.Kind)
	})

	t.Run("MetadataTask", func(t *testing.T) {
		env := envelopeFor(t, "default/watchmen", schema.MetadataTask{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindComic, Name: "watchmen"},
		})
		got := history.Resolve(env)
		require.Equal(t, "Comic", got.Kind)
	})

	t.Run("unrecognised MediaKind leaves Kind empty but keeps the namespace", func(t *testing.T) {
		env := envelopeFor(t, "default/mystery", schema.GrabTask{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKind("podcast"), Name: "mystery"},
		})
		got := history.Resolve(env)
		require.False(t, got.KindKnown())
		require.True(t, got.HasNamespace())
		require.Equal(t, "default", got.Namespace)
	})
}

func TestResolve_SelfContainedRefs(t *testing.T) {
	t.Run("ImportListSynced", func(t *testing.T) {
		env := envelopeFor(t, "irrelevant", schema.ImportListSynced{
			ListRef: schema.Ref{Namespace: "default", Name: "trakt-watchlist"},
		})
		got := history.Resolve(env)
		require.Equal(t, history.Target{
			Namespace: "default", Name: "trakt-watchlist",
			Kind: "ImportList", APIVersion: "catalog.clustarr.io/v1alpha1",
		}, got)
	})

	t.Run("ImportTask resolves a Download, a different group than its own schema prefix", func(t *testing.T) {
		env := envelopeFor(t, "", schema.ImportTask{
			DownloadRef: schema.Ref{Namespace: "default", Name: "dl-abc123"},
		})
		got := history.Resolve(env)
		require.Equal(t, history.Target{
			Namespace: "default", Name: "dl-abc123",
			Kind: "Download", APIVersion: "download.clustarr.io/v1alpha1",
		}, got)
	})

	t.Run("IndexerEvent", func(t *testing.T) {
		env := envelopeFor(t, "", schema.IndexerEvent{
			IndexerRef: schema.Ref{Namespace: "default", Name: "nzbgeek"},
			Action:     "disabled",
		})
		got := history.Resolve(env)
		require.Equal(t, history.Target{
			Namespace: "default", Name: "nzbgeek",
			Kind: "Indexer", APIVersion: "index.clustarr.io/v1alpha1",
		}, got)
	})

	t.Run("RssTask", func(t *testing.T) {
		env := envelopeFor(t, "", schema.RssTask{
			IndexerRef: schema.Ref{Namespace: "default", Name: "nzbgeek"},
		})
		got := history.Resolve(env)
		require.Equal(t, "Indexer", got.Kind)
	})

	t.Run("DownloadEvent", func(t *testing.T) {
		env := envelopeFor(t, "", schema.DownloadEvent{
			DownloadRef: schema.Ref{Namespace: "default", Name: "dl-abc123"},
			Action:      "completed",
		})
		got := history.Resolve(env)
		require.Equal(t, history.Target{
			Namespace: "default", Name: "dl-abc123",
			Kind: "Download", APIVersion: "download.clustarr.io/v1alpha1",
		}, got)
	})

	t.Run("JobEvent", func(t *testing.T) {
		env := envelopeFor(t, "", schema.JobEvent{
			JobRef: schema.Ref{Namespace: "default", Name: "job-1"},
			Action: "succeeded",
		})
		got := history.Resolve(env)
		require.Equal(t, history.Target{
			Namespace: "default", Name: "job-1",
			Kind: "TranscodeJob", APIVersion: "transcode.clustarr.io/v1alpha1",
		}, got)
	})

	t.Run("SubtitleEvent", func(t *testing.T) {
		env := envelopeFor(t, "", schema.SubtitleEvent{
			RequestRef: schema.Ref{Namespace: "default", Name: "req-1"},
			Action:     "downloaded",
			LangKey:    "en",
		})
		got := history.Resolve(env)
		require.Equal(t, history.Target{
			Namespace: "default", Name: "req-1",
			Kind: "SubtitleRequest", APIVersion: "subtitle.clustarr.io/v1alpha1",
		}, got)
	})

	t.Run("FetchTask", func(t *testing.T) {
		env := envelopeFor(t, "", schema.FetchTask{
			RequestRef: schema.Ref{Namespace: "default", Name: "req-1"},
			LangKey:    "en",
		})
		got := history.Resolve(env)
		require.Equal(t, "SubtitleRequest", got.Kind)
	})

	t.Run("ScanTask resolves the LibraryScan, not the RootFolder", func(t *testing.T) {
		env := envelopeFor(t, "", schema.ScanTask{
			LibraryScanRef: schema.Ref{Namespace: "default", Name: "scan-1"},
			RootFolderRef:  schema.Ref{Namespace: "default", Name: "movies"},
			Path:           "/data/movies",
			Mode:           "full",
		})
		got := history.Resolve(env)
		require.Equal(t, history.Target{
			Namespace: "default", Name: "scan-1",
			Kind: "LibraryScan", APIVersion: "catalog.clustarr.io/v1alpha1",
		}, got)
	})
}

// TestResolve_ListTask: an import-list sync that dead-letters names its
// ImportList, so the ImportList is annotated and folds DeadLettered.
func TestResolve_ListTask(t *testing.T) {
	env := envelopeFor(t, "media/", schema.ListTask{ListRef: schema.Ref{Namespace: "media", Name: "trakt-popular"}})
	require.Equal(t, history.Target{
		Namespace: "media", Name: "trakt-popular",
		Kind: "ImportList", APIVersion: "catalog.clustarr.io/v1alpha1",
	}, history.Resolve(env))
}

func TestResolve_NamespaceOnlyAndUnresolvable(t *testing.T) {
	t.Run("WantedScan is a namespace sweep, never a single object", func(t *testing.T) {
		env := envelopeFor(t, "should-be-ignored/too", schema.WantedScan{Namespace: "default"})
		got := history.Resolve(env)
		require.False(t, got.KindKnown())
		require.True(t, got.HasNamespace())
		require.Equal(t, "default", got.Namespace)
		require.Empty(t, got.Name)
	})

	t.Run("Release falls back to the indexer-in-key convention", func(t *testing.T) {
		env := envelopeFor(t, "default/nzbgeek", schema.Release{
			Info:      commonv1.ReleaseInfo{IndexerRef: "nzbgeek"},
			FetchedAt: time.Now(),
		})
		got := history.Resolve(env)
		require.Equal(t, history.Target{
			Namespace: "default", Name: "nzbgeek",
			Kind: "Indexer", APIVersion: "index.clustarr.io/v1alpha1",
		}, got)
	})

	t.Run("a cluster-global payload: no schema entry, no key", func(t *testing.T) {
		env := &events.Envelope{Schema: "some.GlobalTask.v1", Data: []byte(`{"source":"built-in"}`)}
		got := history.Resolve(env)
		require.Equal(t, history.Target{}, got)
		require.False(t, got.KindKnown())
		require.False(t, got.HasNamespace())
	})

	t.Run("an unknown schema degrades to namespace-only from the key", func(t *testing.T) {
		env := &events.Envelope{Schema: "some.FutureEvent.v1", Key: "default/whatever", Data: []byte(`{}`)}
		got := history.Resolve(env)
		require.False(t, got.KindKnown())
		require.Equal(t, "default", got.Namespace)
	})

	t.Run("malformed bytes on a known schema degrade to namespace-only rather than error", func(t *testing.T) {
		env := &events.Envelope{
			Schema: schema.ItemEvent{}.Schema(),
			Key:    "default/broken",
			Data:   []byte(`not json`),
		}
		got := history.Resolve(env)
		require.False(t, got.KindKnown())
		require.Equal(t, "default", got.Namespace)
	})

	t.Run("nil envelope resolves to the zero Target", func(t *testing.T) {
		require.Equal(t, history.Target{}, history.Resolve(nil))
	})
}
