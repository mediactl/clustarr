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
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/status"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// racingReader is the worker's uncached reader with a sibling fetch worker
// in the one window a re-read cannot close: between the read record() seeds
// its apply from and the apply itself. On its first Get it lets the read
// return, then records "es" as downloaded under the same field manager.
type racingReader struct {
	client.Reader
	f     *fixture
	t     *testing.T
	raced bool
}

func (r *racingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := r.Reader.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	if _, ok := obj.(*subtitlev1alpha1.SubtitleRequest); ok && !r.raced {
		r.raced = true
		sib := r.f.get(r.t)
		at := metav1.NewTime(now)
		for i := range sib.Status.Items {
			if it := &sib.Status.Items[i]; it.LangKey == "es" {
				it.State = subtitlev1alpha1.SubtitleItemDownloaded
				it.Score, it.ScoreOutOf = 170, 180
				it.Provider, it.SubtitleID, it.Path = "os-sibling", "sib-1", "Film (2010).es.srt"
				it.DownloadedAt = &at
			}
		}
		require.NoError(r.t, status.PatchRequest(ctx, r.f.c, k8s.ManagerCaptionarrWorker, sib, nil))
	}
	return nil
}

// Two languages of one SubtitleRequest fetched at once, by two workers: each
// records its own item, and status.RequestWorkerFields re-declares every
// worker leaf of EVERY live item, so an apply seeded from a read the other
// had since overtaken re-sent the other's item as it was before -- a written
// sidecar rolled back to pending, then dropped from the MediaFile's
// sidecars. The re-read before the apply narrowed that window and could not
// close it; record() now applies with the read's resourceVersion and redoes
// set() on a fresh read after a Conflict.
func TestAFetchDoesNotRollBackASiblingRecordedBetweenItsReadAndApply(t *testing.T) {
	f := newFixture(t, natsBus(t))
	f.seedLive(t, "en", "es")
	f.worker.APIReader = &racingReader{Reader: f.c, f: f, t: t}

	p := newFakeProvider("fake")
	p.cands = []subtitles.Candidate{candidate("exact", releaseTitle)}
	p.files["exact"] = []byte(srtWithHI)
	f.entry("os", os1, p)

	require.NoError(t, f.worker.Handle(f.ctx, f.message(t, "en", nil)))

	es := f.item(t, "es")
	assert.Equal(t, subtitlev1alpha1.SubtitleItemDownloaded, es.State, "the sibling's download was rolled back")
	assert.Equal(t, "Film (2010).es.srt", es.Path, "the sibling's sidecar path was rolled back")

	en := f.item(t, "en")
	assert.Equal(t, sidecarName, en.Path, "and this fetch still recorded its own")
}

// And without a contrived window: both languages fetched at once, both
// recorded.
func TestTwoLanguagesFetchedConcurrentlyAreBothRecorded(t *testing.T) {
	f := newFixture(t, natsBus(t))
	f.seedLive(t, "en", "es")

	p := newFakeProvider("fake")
	spanish := candidate("exact-es", releaseTitle)
	spanish.Language = "es"
	p.cands = []subtitles.Candidate{candidate("exact", releaseTitle), spanish}
	p.files["exact"] = []byte(srtWithHI)
	p.files["exact-es"] = []byte(srtWithHI)
	f.entry("os", os1, p)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, lang := range []string{"en", "es"} {
		msg := f.message(t, lang, nil)
		wg.Go(func() { errs[i] = f.worker.Handle(f.ctx, msg) })
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])

	for _, lang := range []string{"en", "es"} {
		it := f.item(t, lang)
		assert.NotEmpty(t, it.Path, "%s was fetched but not recorded", lang)
		assert.NotNil(t, it.DownloadedAt, "%s was fetched but not recorded", lang)
	}
}
