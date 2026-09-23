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

package fetch_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/captionarr/throttle"
	"github.com/mediactl/clustarr/captionarr/worker/fetch"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// R-4, Bazarr's pooling: every provider is searched and the best candidate
// of all of them is downloaded. The higher-priority provider's 132 clears
// the 126 minimum, and before gap-fix X11b it won outright -- the second
// provider was never even asked. Now the second provider's 147 wins, and
// the first provider's download quota is not touched.
func TestPoolingDownloadsTheBestCandidateOfEveryProvider(t *testing.T) {
	f := newFixture(t, natsBus(t))

	first := newFakeProvider("fake-a")
	first.cands = []subtitles.Candidate{candidate("a-other", "Film.2010.1080p.BluRay.x264-OTHER")} // 132
	first.files["a-other"] = []byte(srtWithHI)
	second := newFakeProvider("fake-b")
	second.cands = []subtitles.Candidate{candidate("b-exact", releaseTitle)} // 147
	second.files["b-exact"] = []byte(srtWithHI)
	f.entry("os-a", os1, first)
	f.entry("os-b", os1, second)

	require.NoError(t, f.worker.Handle(f.ctx, f.message(t, "en", nil)))

	it := f.item(t, "en")
	assert.Equal(t, "os-b", it.Provider, "the pool's best, whichever provider offered it")
	assert.Equal(t, "b-exact", it.SubtitleID)
	assert.Equal(t, int32(147), it.Score)
	assert.Equal(t, int32(1), first.searches.Load(), "every provider is searched")
	assert.Equal(t, int32(1), second.searches.Load())
	assert.Equal(t, int32(0), first.downloads.Load(), "only the best candidate is downloaded")
	assert.Equal(t, int32(1), second.downloads.Load())
}

// Two SubtitleProviders of one type -- two accounts, whose clients share a
// Name() -- are both searched. When the first's download quota is spent,
// that provider is benched (for the rest of the pool and, through the
// shared throttle, for every worker) and the pool falls through to the same
// subtitle offered by the second account. Before gap-fix X11b a registry
// keyed by provider type skipped the second account entirely.
func TestASecondAccountOfOneTypeBacksTheFirstsDownloadQuota(t *testing.T) {
	f := newFixture(t, natsBus(t))

	resetAt := now.Add(3 * time.Hour)
	mainAcct := newFakeProvider("opensubtitlescom")
	mainAcct.cands = []subtitles.Candidate{candidate("exact", releaseTitle)}
	mainAcct.dlErr["exact"] = &subtitles.ProviderError{
		Provider: "opensubtitlescom", Kind: subtitles.KindDownloadLimitExceeded, Remaining: 0, ResetAt: resetAt,
	}
	spare := newFakeProvider("opensubtitlescom")
	spare.cands = []subtitles.Candidate{candidate("exact", releaseTitle)}
	spare.files["exact"] = []byte(srtWithHI)
	m := f.entry("os-main", os1, mainAcct)
	f.entry("os-spare", os1, spare)

	require.NoError(t, f.worker.Handle(f.ctx, f.message(t, "en", nil)))

	it := f.item(t, "en")
	assert.Equal(t, subtitlev1alpha1.SubtitleItemUpgradable, it.State)
	assert.Equal(t, "os-spare", it.Provider, "the second account served the subtitle the first could not")
	assert.Equal(t, int32(147), it.Score)
	assert.Equal(t, int32(1), mainAcct.downloads.Load())
	assert.Equal(t, int32(1), spare.downloads.Load())
	assert.Empty(t, it.LastError, "a written subtitle carries no error")

	st, err := throttle.Get(f.ctx, f.bus.KV(events.BucketProviderThrottle), m.UID)
	require.NoError(t, err)
	require.True(t, st.Throttled(now), "the spent account is benched for every worker")
	assert.Equal(t, subtitles.KindDownloadLimitExceeded, st.ThrottleReason)
	require.NotNil(t, st.Quota)
	assert.Equal(t, int32(0), st.Quota.Remaining)
}

// spec.embedded.extract "writes a matching embedded track out as a sidecar
// instead of searching providers for it": local providers are a tier of
// their own, searched first, so an extractable track that reaches the
// threshold is written without a single remote request -- even when the
// embedded SubtitleProvider has the lower priority.
func TestAnExtractableEmbeddedTrackIsWrittenWithoutAskingARemoteProvider(t *testing.T) {
	f := newFixture(t, natsBus(t))
	f.probed(t, commonv1.MediaInfo{
		Container: "mkv",
		Subtitles: []commonv1.SubtitleStream{{Index: 2, Codec: "subrip", Language: "eng"}},
	})

	remote := newFakeProvider("fake-os")
	remote.cands = []subtitles.Candidate{candidate("exact", releaseTitle)}
	remote.files["exact"] = []byte(srtWithHI)
	local := newFakeProvider("embedded")
	local.caps.HashVerifiable = false // pkg/subtitles/providers/embedded's own claim
	local.cands = []subtitles.Candidate{{
		FetchID: "2", Language: "eng",
		Matches: map[string]bool{subtitles.MatchHash: true, subtitles.MatchHearingImpaired: true},
	}}
	local.files["2"] = []byte(srtWithHI)
	f.entry("os", os1, remote)      // priority 1
	f.localEntry("embedded", local) // priority 2

	require.NoError(t, f.worker.Handle(f.ctx, f.message(t, "en", nil)))

	it := f.item(t, "en")
	assert.Equal(t, "embedded", it.Provider)
	assert.Equal(t, "2", it.SubtitleID, "the stream index")
	assert.Equal(t, int32(179), it.Score, "the file's own track: the hash weight")
	assert.Equal(t, subtitlev1alpha1.SubtitleItemDownloaded, it.State, "within 3 of 180: nothing to upgrade")
	assert.Equal(t, int32(1), local.downloads.Load())
	assert.Equal(t, int32(0), remote.searches.Load(), "no remote provider is asked when extraction wrote the sidecar")
}

// A sidecar is written with its RootFolder's spec.permissions.fileMode, the
// mode importarr gave the video beside it -- not the fixed 0664 every
// sidecar got before gap-fix X11b. (With no RootFolder around the file the
// default holds; TestFetchWritesTheBestAcceptableSubtitleAndRecordsIt
// asserts it.)
func TestTheSidecarTakesItsRootFoldersFileMode(t *testing.T) {
	f := newFixture(t, natsBus(t))
	require.NoError(t, f.c.Create(f.ctx, &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: f.ns},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: "/data/media/movies", Kind: catalogv1alpha1.RootFolderKindMovie,
			Permissions: catalogv1alpha1.Perms{FileMode: "0640"},
		},
	}))

	p := newFakeProvider("fake")
	p.cands = []subtitles.Candidate{candidate("exact", releaseTitle)}
	p.files["exact"] = []byte(srtWithHI)
	f.entry("os", os1, p)

	require.NoError(t, f.worker.Handle(f.ctx, f.message(t, "en", nil)))

	st, err := os.Stat(f.local(filepath.Join(filepath.Dir(mediaLogical), sidecarName)))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), st.Mode().Perm())
	assert.NotEqual(t, fetch.DefaultSidecarMode, st.Mode().Perm())
}

// A task for a MediaFile whose Movie is gone -- the file an import list's
// removeAndKeep kept (gap-fix X7b), published before the item went -- asks
// no provider and records nothing. The controller blocks the request, so no
// further task follows.
func TestATaskForAFileWhoseItemIsGoneFetchesNothing(t *testing.T) {
	f := newFixture(t, natsBus(t))
	p := newFakeProvider("fake")
	p.cands = []subtitles.Candidate{candidate("exact", releaseTitle)}
	p.files["exact"] = []byte(srtWithHI)
	f.entry("os", os1, p)
	m := f.message(t, "en", nil)

	require.NoError(t, f.c.Delete(f.ctx, &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: "film", Namespace: f.ns}}))
	before := f.get(t).ResourceVersion
	require.NoError(t, f.worker.Handle(f.ctx, m), "acked, not redelivered")

	assert.Equal(t, before, f.get(t).ResourceVersion, "nothing recorded")
	assert.Equal(t, int32(0), p.searches.Load(), "no provider is asked for a file Clustarr no longer manages")
	_, err := os.Stat(f.local(filepath.Join(filepath.Dir(mediaLogical), sidecarName)))
	assert.True(t, os.IsNotExist(err), "no sidecar written")
	assert.Empty(t, f.bus.subtitleEvents(t))
}
