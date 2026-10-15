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

package pipeline_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/pipeline"
)

// movieWith builds a minimal Movie at generation 1 with cached metadata
// already populated, and applies opts on top -- mirroring the shape the task
// brief's own table test uses.
func movieWith(t *testing.T, opts ...func(*catalogv1.Movie)) *catalogv1.Movie {
	t.Helper()
	m := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "shawshank-redemption",
			Namespace:  "default",
			Generation: 1,
		},
		Status: catalogv1.MovieStatus{
			Metadata: &catalogv1.MovieMetadata{Title: "The Shawshank Redemption"},
		},
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// ready marks condType True, observed at the movie's current generation.
func ready(condType string) func(*catalogv1.Movie) {
	return func(m *catalogv1.Movie) {
		k8s.MarkTrue(m, &m.Status.Conditions, condType, "Reconciled", "ready")
	}
}

// notReady marks condType False.
func notReady(condType string) func(*catalogv1.Movie) {
	return func(m *catalogv1.Movie) {
		k8s.MarkFalse(m, &m.Status.Conditions, condType, "Pending", "not ready yet")
	}
}

// staleReady marks condType True and then advances the movie's generation,
// so the condition is True but no longer reflects the current spec -- the
// "found, not yet synced" state.
func staleReady(condType string) func(*catalogv1.Movie) {
	return func(m *catalogv1.Movie) {
		k8s.MarkTrue(m, &m.Status.Conditions, condType, "Reconciled", "ready")
		m.Generation++
	}
}

func TestProjectDerivesTheStage(t *testing.T) {
	tests := []struct {
		name    string
		movie   *catalogv1.Movie
		related pipeline.Related
		want    pipeline.Stage
	}{
		// --- metadata -----------------------------------------------------
		{
			name:  "no metadata yet",
			movie: movieWith(t, notReady("MetadataReady")),
			want:  pipeline.StageMetadataSearching,
		},
		{
			name:  "metadata ready but stale relative to the current generation",
			movie: movieWith(t, staleReady(catalogv1.MovieConditionMetadataReady)),
			want:  pipeline.StageMetadataFound,
		},
		{
			name:  "metadata ready and synced",
			movie: movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			want:  pipeline.StageMetadataSynced,
		},

		// --- release --------------------------------------------------------
		{
			name:  "metadata ready, search running",
			movie: movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: pipeline.Related{
				Search: &catalogv1.Search{Status: catalogv1.SearchStatus{Phase: catalogv1.SearchPhaseRunning}},
			},
			want: pipeline.StageReleaseSearching,
		},
		{
			name:  "search completed, nothing grabbed yet",
			movie: movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: pipeline.Related{
				Search: &catalogv1.Search{Status: catalogv1.SearchStatus{Phase: catalogv1.SearchPhaseCompleted}},
			},
			want: pipeline.StageReleaseSelected,
		},

		// --- download and import ---------------------------------------------
		{
			name:  "download in flight",
			movie: movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: pipeline.Related{
				Entries: []catalogv1.DownloadEntry{{Phase: commonv1.DownloadPhaseDownloading}},
			},
			want: pipeline.StageDownloading,
		},
		{
			name:  "download completed, import not started",
			movie: movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: pipeline.Related{
				Entries: []catalogv1.DownloadEntry{{Phase: commonv1.DownloadPhaseCompleted}},
			},
			want: pipeline.StageDownloaded,
		},
		{
			name:  "import in progress",
			movie: movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: pipeline.Related{
				Entries: []catalogv1.DownloadEntry{{
					Phase:  commonv1.DownloadPhaseCompleted,
					Import: &catalogv1.DownloadImportSummary{Phase: catalogv1.ImportPhaseApproved},
				}},
			},
			want: pipeline.StageImporting,
		},
		{
			name:  "imported, nothing downstream started yet",
			movie: movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: pipeline.Related{
				MediaFile: &catalogv1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: "shawshank-redemption"}},
			},
			want: pipeline.StageImported,
		},

		// --- subtitles: status.subtitles on the file ---------------------------
		{
			name:    "subtitles wanted, nothing on disk yet",
			movie:   movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: withFile(subs(catalogv1.SubtitlesPhaseWanted), planned()),
			want:    pipeline.StageSubtitleSearching,
		},
		{
			name:  "one language on disk, another still wanted",
			movie: movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: withFile(subs(catalogv1.SubtitlesPhaseSearching,
				catalogv1.SubtitleItemStatus{LangKey: "en", State: catalogv1.SubtitleStateUpgradable},
				catalogv1.SubtitleItemStatus{LangKey: "es", State: catalogv1.SubtitleStatePending}), planned()),
			want: pipeline.StageSubtitleFound,
		},
		{
			name:    "subtitles satisfied, the transcode still waiting for a slot",
			movie:   movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: withFile(subs(catalogv1.SubtitlesPhaseSatisfied), planned()),
			want:    pipeline.StageSubtitleDone,
		},

		// --- transcode: status.transcode on the file -----------------------------
		{
			name:    "a running transcode, with its percent",
			movie:   movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: withPercent(withFile(subs(catalogv1.SubtitlesPhaseWanted), &catalogv1.TranscodeState{Phase: catalogv1.TranscodePhaseRunning}), 42),
			want:    pipeline.StageTranscoding,
		},
		{
			name:    "swapped, subtitles still wanted",
			movie:   movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: withFile(subs(catalogv1.SubtitlesPhaseWanted), &catalogv1.TranscodeState{Phase: catalogv1.TranscodePhaseSucceeded}),
			want:    pipeline.StageTranscodeDone,
		},

		// --- complete, failed, blocked ------------------------------------------
		{
			name:    "the profile skipped it and it wants no subtitles: complete",
			movie:   movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: withFile(subs(catalogv1.SubtitlesPhaseNotWanted), &catalogv1.TranscodeState{Phase: catalogv1.TranscodePhaseSkipped, Reason: "Transcoded"}),
			want:    pipeline.StageComplete,
		},
		{
			name:    "subtitles blocked",
			movie:   movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: withFile(&catalogv1.SubtitlesStatus{Phase: catalogv1.SubtitlesPhaseBlocked, Reason: "NoProfile"}, nil),
			want:    pipeline.StageBlocked,
		},
		{
			name:  "failure wins over everything",
			movie: movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: pipeline.Related{
				Entries: []catalogv1.DownloadEntry{{Phase: commonv1.DownloadPhaseFailed}},
			},
			want: pipeline.StageFailed,
		},
		{
			name:  "blocklisted release blocks the item",
			movie: movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: pipeline.Related{
				Entries: []catalogv1.DownloadEntry{{Phase: commonv1.DownloadPhaseBlocklisted}},
			},
			want: pipeline.StageBlocked,
		},
	}

	// Every Stage constant must be exercised by exactly one case above, so a
	// stage that silently stops being reachable fails this test rather than
	// the page.
	seen := make(map[pipeline.Stage]bool, len(tests))
	for _, tc := range tests {
		seen[tc.want] = true
	}
	for _, s := range []pipeline.Stage{
		pipeline.StageMetadataSearching, pipeline.StageMetadataFound, pipeline.StageMetadataSynced,
		pipeline.StageReleaseSearching, pipeline.StageReleaseSelected,
		pipeline.StageDownloading, pipeline.StageDownloaded,
		pipeline.StageImporting, pipeline.StageImported,
		pipeline.StageSubtitleSearching, pipeline.StageSubtitleFound, pipeline.StageSubtitleDone,
		pipeline.StageTranscoding, pipeline.StageTranscodeDone,
		pipeline.StageComplete, pipeline.StageFailed, pipeline.StageBlocked,
	} {
		require.True(t, seen[s], "stage %s has no test case", s)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry := pipeline.Project(tc.movie, tc.related)
			require.Equal(t, tc.want, entry.Stage)
		})
	}
}

// subs is a file's status.subtitles block.
func subs(phase catalogv1.SubtitlesPhase, items ...catalogv1.SubtitleItemStatus) *catalogv1.SubtitlesStatus {
	return &catalogv1.SubtitlesStatus{Phase: phase, Items: items}
}

// planned is a transcode block waiting for admission.
func planned() *catalogv1.TranscodeState {
	return &catalogv1.TranscodeState{Phase: catalogv1.TranscodePhasePlanned}
}

// withFile relates the item to its MediaFile carrying the given blocks.
func withFile(s *catalogv1.SubtitlesStatus, tr *catalogv1.TranscodeState) pipeline.Related {
	return pipeline.Related{MediaFile: &catalogv1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "shawshank-redemption"},
		Status:     catalogv1.MediaFileStatus{Subtitles: s, Transcode: tr},
	}}
}

// withPercent sets a running transcode's clustarr-progress percent.
func withPercent(r pipeline.Related, p int32) pipeline.Related {
	r.TranscodePercent = &p
	return r
}

// albumWith builds a minimal, metadata-synced Album -- a non-video kind that
// never goes through transcode or caption.
func albumWith(t *testing.T) *catalogv1.Album {
	t.Helper()
	a := &catalogv1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "boxer", Namespace: "default", Generation: 1},
		Status:     catalogv1.AlbumStatus{Metadata: &catalogv1.AlbumMetadata{Title: "Boxer"}},
	}
	k8s.MarkTrue(a, &a.Status.Conditions, catalogv1.AlbumConditionMetadataReady, "Reconciled", "ready")
	return a
}

// bookWith builds a minimal, metadata-synced Book -- another non-video kind.
func bookWith(t *testing.T) *catalogv1.Book {
	t.Helper()
	b := &catalogv1.Book{
		ObjectMeta: metav1.ObjectMeta{Name: "the-hobbit", Namespace: "default", Generation: 1},
		Status:     catalogv1.BookStatus{Metadata: &catalogv1.BookMetadata{Title: "The Hobbit"}},
	}
	k8s.MarkTrue(b, &b.Status.Conditions, catalogv1.BookConditionMetadataReady, "Reconciled", "ready")
	return b
}

// TestProjectNonVideoKindsCompleteOnImport covers the bug the review found:
// Album, Artist, Author, Book, Audiobook, Comic and Issue never get a
// SubtitleRequest or a TranscodeJob (transcode and caption only watch
// video MediaFiles), so requiring either of those to exist and be terminal
// before StageComplete -- correct for Movie/Series/Episode -- left every
// other kind stuck at StageImported forever. For these kinds, being imported
// is the whole job.
func TestProjectNonVideoKindsCompleteOnImport(t *testing.T) {
	tests := []struct {
		name string
		item client.Object
	}{
		{name: "album", item: albumWith(t)},
		{name: "book", item: bookWith(t)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry := pipeline.Project(tc.item, pipeline.Related{
				MediaFile: &catalogv1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: tc.item.GetName()}},
			})
			require.Equal(t, pipeline.StageComplete, entry.Stage)
			require.EqualValues(t, 100, entry.Percent)
		})
	}
}

func TestProjectFillsInTheCommonFields(t *testing.T) {
	movie := movieWith(t, ready(catalogv1.MovieConditionMetadataReady))
	entry := pipeline.Project(movie, pipeline.Related{
		Entries: []catalogv1.DownloadEntry{{
			Phase: commonv1.DownloadPhaseDownloading,
			Stage: commonv1.DownloadStageTransferring,
		}},
	})

	require.Equal(t, "shawshank-redemption", entry.Ref.Name)
	require.Equal(t, "default", entry.Ref.Namespace)
	require.Equal(t, "The Shawshank Redemption", entry.Title)
	require.Equal(t, pipeline.StageDownloading, entry.Stage)
	require.Equal(t, string(commonv1.DownloadStageTransferring), entry.Detail)
	// A grab entry carries no byte counters (they are clustarr-progress'),
	// so the percent stays unknown and the ui reads the 1 Hz record.
	require.EqualValues(t, -1, entry.Percent)
}

// TestProjectMapsEveryDownloadPhase is gap-fix ruling R-12's re-read of the
// pipeline's grab mapping against every real DownloadPhase (task X14), now
// over the owner's grab entries (ADR-0019 §6.2, A9.1), with rows read from
// the generated CRD's status.downloads[].phase enum so a phase added later
// without a decision fails here. An Imported entry is the item imported,
// whether or not its MediaFile is in the cache yet.
func TestProjectMapsEveryDownloadPhase(t *testing.T) {
	item := movieWith(t, ready(catalogv1.MovieConditionMetadataReady))
	// What the item shows with no entry at all: a phase with no download
	// stage falls through to it.
	fallback := pipeline.Project(item, pipeline.Related{}).Stage
	want := map[commonv1.DownloadPhase]pipeline.Stage{
		// The grab created it and the downloads stage has not admitted it
		// yet: the item is downloading, not still at ReleaseSelected.
		"":                                pipeline.StageDownloading,
		commonv1.DownloadPhasePending:     pipeline.StageDownloading,
		commonv1.DownloadPhaseAssigned:    pipeline.StageDownloading,
		commonv1.DownloadPhaseQueued:      pipeline.StageDownloading,
		commonv1.DownloadPhaseDownloading: pipeline.StageDownloading,
		commonv1.DownloadPhasePaused:      pipeline.StageDownloading,
		commonv1.DownloadPhaseCompleted:   pipeline.StageDownloaded,
		commonv1.DownloadPhaseSeeding:     pipeline.StageDownloaded,
		commonv1.DownloadPhaseImported:    pipeline.StageImported,
		commonv1.DownloadPhaseFailed:      pipeline.StageFailed,
		commonv1.DownloadPhaseBlocklisted: pipeline.StageBlocked,
		// Being torn down: no download stage; the item's own state shows.
		commonv1.DownloadPhaseRemoving: fallback,
	}
	for phase, stage := range want {
		t.Run(string(phase), func(t *testing.T) {
			entry := pipeline.Project(item, pipeline.Related{
				Entries: []catalogv1.DownloadEntry{{Phase: phase}},
			})
			require.Equal(t, stage, entry.Stage)
		})
	}

	// Every phase the CRD admits on a grab entry has a row above.
	raw, err := os.ReadFile("../../config/crd/bases/catalog.clustarr.io_movies.yaml")
	require.NoError(t, err)
	var crd apiextensionsv1.CustomResourceDefinition
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	require.NotEmpty(t, crd.Spec.Versions)
	downloads := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["status"].Properties["downloads"]
	require.NotNil(t, downloads.Items, "status.downloads is a list")
	require.NotNil(t, downloads.Items.Schema)
	enum := downloads.Items.Schema.Properties["phase"].Enum
	require.NotEmpty(t, enum)
	for _, v := range enum {
		var p string
		require.NoError(t, json.Unmarshal(v.Raw, &p))
		_, ok := want[commonv1.DownloadPhase(p)]
		require.True(t, ok, "DownloadPhase %q has no row: decide which pipeline stage it shows", p)
	}
}

// A running transcode's note says what it does: a remux copies the video,
// never reading as FFmpeg's "copy" encoder (owner, 2026-10-15).
func TestARunningTranscodeSaysWhatItDoes(t *testing.T) {
	movie := movieWith(t, ready(catalogv1.MovieConditionMetadataReady))
	for _, plan := range []*catalogv1.TranscodePlan{
		{Mode: catalogv1.TranscodeModeRemuxOnly, Encoder: "copy", VideoAction: "copy"},
		{Mode: catalogv1.TranscodeModeTranscode, Encoder: "hevc_qsv", VideoAction: "encode"},
	} {
		entry := pipeline.Project(movie, withFile(subs(catalogv1.SubtitlesPhaseWanted),
			&catalogv1.TranscodeState{Phase: catalogv1.TranscodePhaseRunning, Plan: plan}))
		require.Equal(t, pipeline.StageTranscoding, entry.Stage)
		require.Equal(t, plan.Describe(), entry.Detail)
	}
}
