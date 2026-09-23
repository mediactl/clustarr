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
	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	subtitlev1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
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
				Downloads: []downloadv1.Download{{Status: downloadv1.DownloadStatus{Phase: downloadv1.DownloadPhaseDownloading}}},
			},
			want: pipeline.StageDownloading,
		},
		{
			name:  "download completed, import not started",
			movie: movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: pipeline.Related{
				Downloads: []downloadv1.Download{{Status: downloadv1.DownloadStatus{Phase: downloadv1.DownloadPhaseCompleted}}},
			},
			want: pipeline.StageDownloaded,
		},
		{
			name:  "import in progress",
			movie: movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: pipeline.Related{
				Downloads: []downloadv1.Download{{Status: downloadv1.DownloadStatus{
					Phase:  downloadv1.DownloadPhaseCompleted,
					Import: &downloadv1.ImportState{State: downloadv1.ImportPhaseImporting},
				}}},
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

		// --- subtitles --------------------------------------------------------
		{
			name:  "subtitle wanted, no search underway",
			movie: movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: pipeline.Related{
				MediaFile: &catalogv1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: "shawshank-redemption"}},
				Subtitles: []subtitlev1.SubtitleRequest{{Status: subtitlev1.SubtitleRequestStatus{
					Phase: subtitlev1.SubtitleRequestPhaseWanted,
				}}},
			},
			want: pipeline.StageSubtitleSearching,
		},
		{
			name:  "subtitle found on disk but request not yet satisfied",
			movie: movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: pipeline.Related{
				MediaFile: &catalogv1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: "shawshank-redemption"}},
				Subtitles: []subtitlev1.SubtitleRequest{{Status: subtitlev1.SubtitleRequestStatus{
					Phase: subtitlev1.SubtitleRequestPhaseSearching,
					Items: []subtitlev1.SubtitleItem{{LangKey: "eng", State: subtitlev1.SubtitleItemUpgradable}},
				}}},
			},
			want: pipeline.StageSubtitleFound,
		},
		{
			name:  "subtitle candidate chosen and downloading",
			movie: movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: pipeline.Related{
				MediaFile: &catalogv1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: "shawshank-redemption"}},
				Subtitles: []subtitlev1.SubtitleRequest{{Status: subtitlev1.SubtitleRequestStatus{
					Phase: subtitlev1.SubtitleRequestPhaseSearching,
					Items: []subtitlev1.SubtitleItem{{
						LangKey:  "eng",
						State:    subtitlev1.SubtitleItemSearching,
						Provider: "opensubtitles",
					}},
				}}},
			},
			want: pipeline.StageSubtitleFetching,
		},
		{
			name:  "subtitle satisfied, no transcode job",
			movie: movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: pipeline.Related{
				MediaFile: &catalogv1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: "shawshank-redemption"}},
				Subtitles: []subtitlev1.SubtitleRequest{{Status: subtitlev1.SubtitleRequestStatus{
					Phase: subtitlev1.SubtitleRequestPhaseSatisfied,
				}}},
			},
			want: pipeline.StageSubtitleDone,
		},

		// --- transcode --------------------------------------------------------
		{
			name:  "transcode outranks a finished download",
			movie: movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: pipeline.Related{
				Downloads: []downloadv1.Download{{Status: downloadv1.DownloadStatus{Phase: downloadv1.DownloadPhaseCompleted}}},
				Jobs:      []transcodev1.TranscodeJob{{Status: transcodev1.TranscodeJobStatus{Phase: transcodev1.TranscodeJobPhaseRunning}}},
			},
			want: pipeline.StageTranscoding,
		},
		{
			name:  "transcode succeeded, no subtitle request",
			movie: movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: pipeline.Related{
				MediaFile: &catalogv1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: "shawshank-redemption"}},
				Jobs:      []transcodev1.TranscodeJob{{Status: transcodev1.TranscodeJobStatus{Phase: transcodev1.TranscodeJobPhaseSucceeded}}},
			},
			want: pipeline.StageTranscodeDone,
		},

		// --- complete, failed, blocked ------------------------------------------
		{
			name:  "imported, subtitled and transcoded",
			movie: movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: pipeline.Related{
				MediaFile: &catalogv1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: "shawshank-redemption"}},
				Subtitles: []subtitlev1.SubtitleRequest{{Status: subtitlev1.SubtitleRequestStatus{
					Phase: subtitlev1.SubtitleRequestPhaseSatisfied,
				}}},
				Jobs: []transcodev1.TranscodeJob{{Status: transcodev1.TranscodeJobStatus{Phase: transcodev1.TranscodeJobPhaseSucceeded}}},
			},
			want: pipeline.StageComplete,
		},
		{
			name:  "failure wins over everything",
			movie: movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: pipeline.Related{
				Downloads: []downloadv1.Download{{Status: downloadv1.DownloadStatus{Phase: downloadv1.DownloadPhaseFailed}}},
			},
			want: pipeline.StageFailed,
		},
		{
			name:  "blocklisted release blocks the item",
			movie: movieWith(t, ready(catalogv1.MovieConditionMetadataReady)),
			related: pipeline.Related{
				Downloads: []downloadv1.Download{{Status: downloadv1.DownloadStatus{Phase: downloadv1.DownloadPhaseBlocklisted}}},
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
		pipeline.StageSubtitleSearching, pipeline.StageSubtitleFound, pipeline.StageSubtitleFetching, pipeline.StageSubtitleDone,
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

// albumWith builds a minimal, metadata-synced Album -- a non-video kind that
// never goes through squasharr or captionarr.
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
// SubtitleRequest or a TranscodeJob (squasharr and captionarr only watch
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

// TestProjectMovieWithNoTranscodeJobDoesNotReachComplete is the video-kind
// counterpart of the fix above: a Movie (which does go through squasharr)
// must NOT report StageComplete just because it was imported and its
// subtitles are satisfied -- it still needs a TranscodeJob to exist and be
// terminal.
func TestProjectMovieWithNoTranscodeJobDoesNotReachComplete(t *testing.T) {
	movie := movieWith(t, ready(catalogv1.MovieConditionMetadataReady))
	entry := pipeline.Project(movie, pipeline.Related{
		MediaFile: &catalogv1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: "shawshank-redemption"}},
		Subtitles: []subtitlev1.SubtitleRequest{{Status: subtitlev1.SubtitleRequestStatus{
			Phase: subtitlev1.SubtitleRequestPhaseSatisfied,
		}}},
	})
	require.NotEqual(t, pipeline.StageComplete, entry.Stage)
	require.Equal(t, pipeline.StageSubtitleDone, entry.Stage)
}

func TestProjectFillsInTheCommonFields(t *testing.T) {
	movie := movieWith(t, ready(catalogv1.MovieConditionMetadataReady))
	entry := pipeline.Project(movie, pipeline.Related{
		Downloads: []downloadv1.Download{{Status: downloadv1.DownloadStatus{
			Phase:           downloadv1.DownloadPhaseDownloading,
			ProgressPercent: 42,
		}}},
	})

	require.Equal(t, "shawshank-redemption", entry.Ref.Name)
	require.Equal(t, "default", entry.Ref.Namespace)
	require.Equal(t, "The Shawshank Redemption", entry.Title)
	require.Equal(t, pipeline.StageDownloading, entry.Stage)
	require.EqualValues(t, 42, entry.Percent)
}

// TestProjectMapsEveryDownloadPhase is gap-fix ruling R-12's re-read of the
// pipeline's Download mapping against every real DownloadPhase (task X14),
// with rows read from the generated CRD's enum so a phase added later
// without a decision fails here. An imported Download is decided by its
// status.import (or its MediaFile), not its phase, so an Imported phase
// with neither shows no Download stage of its own.
func TestProjectMapsEveryDownloadPhase(t *testing.T) {
	item := movieWith(t, ready(catalogv1.MovieConditionMetadataReady))
	// What the item shows with no Download at all: a phase with no Download
	// stage falls through to it.
	fallback := pipeline.Project(item, pipeline.Related{}).Stage
	want := map[downloadv1.DownloadPhase]pipeline.Stage{
		// The grab created it and grabarr has not reconciled it yet: the
		// item is downloading, not still at ReleaseSelected.
		"":                                  pipeline.StageDownloading,
		downloadv1.DownloadPhasePending:     pipeline.StageDownloading,
		downloadv1.DownloadPhaseAssigned:    pipeline.StageDownloading,
		downloadv1.DownloadPhaseQueued:      pipeline.StageDownloading,
		downloadv1.DownloadPhaseDownloading: pipeline.StageDownloading,
		downloadv1.DownloadPhasePaused:      pipeline.StageDownloading,
		downloadv1.DownloadPhaseCompleted:   pipeline.StageDownloaded,
		downloadv1.DownloadPhaseSeeding:     pipeline.StageDownloaded,
		downloadv1.DownloadPhaseImported:    fallback,
		downloadv1.DownloadPhaseFailed:      pipeline.StageFailed,
		downloadv1.DownloadPhaseBlocklisted: pipeline.StageBlocked,
		// Being torn down: no Download stage; the item's own state shows.
		downloadv1.DownloadPhaseRemoving: fallback,
	}
	for phase, stage := range want {
		t.Run(string(phase), func(t *testing.T) {
			entry := pipeline.Project(item, pipeline.Related{
				Downloads: []downloadv1.Download{{Status: downloadv1.DownloadStatus{Phase: phase}}},
			})
			require.Equal(t, stage, entry.Stage)
		})
	}

	// Every phase the CRD admits has a row above.
	raw, err := os.ReadFile("../../config/crd/bases/download.clustarr.io_downloads.yaml")
	require.NoError(t, err)
	var crd apiextensionsv1.CustomResourceDefinition
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	require.NotEmpty(t, crd.Spec.Versions)
	enum := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["status"].Properties["phase"].Enum
	require.NotEmpty(t, enum)
	for _, v := range enum {
		var p string
		require.NoError(t, json.Unmarshal(v.Raw, &p))
		_, ok := want[downloadv1.DownloadPhase(p)]
		require.True(t, ok, "DownloadPhase %q has no row: decide which pipeline stage it shows", p)
	}
}
