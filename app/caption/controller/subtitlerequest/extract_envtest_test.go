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

package subtitlerequest_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
)

// embeddedProvider creates an enabled embedded SubtitleProvider in the
// fixture's namespace: the extractor spec.embedded.extract needs.
func (f *fixture) embeddedProvider(name string) *subtitlev1alpha1.SubtitleProvider {
	f.t.Helper()
	sp := &subtitlev1alpha1.SubtitleProvider{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns},
		Spec:       subtitlev1alpha1.SubtitleProviderSpec{Type: subtitlev1alpha1.SubtitleProviderEmbedded, Enabled: ptr.To(true)},
	}
	require.NoError(f.t, f.c.Create(f.ctx, sp))
	return sp
}

func dispatched(f *fixture) []string {
	var out []string
	for _, c := range f.bus.stored() {
		out = append(out, c.task.LangKey)
	}
	slices.Sort(out)
	return out
}

// spec.embedded.extract, given its effect (gap-fix X11b). The profile wants
// en and de; the file carries an English SubRip track. With extract on (the
// CRD default) and an embedded SubtitleProvider to do it, the track is a
// want -- not status.existing -- so a fetch task goes out for en, which the
// worker's local tier fills by writing the track out. With extract off it
// counts as existing, as design spec §6.5 has it, and only de is wanted.
func TestExtractMakesAnEmbeddedTextTrackAWant(t *testing.T) {
	f := newFixture(t, "sr-extract")
	p := f.profile(nil)
	require.True(t, p.Spec.Embedded.ExtractOrDefault(), "setup: extract defaults on")
	f.embeddedProvider("embedded")
	f.mediaFile("movie", englishTrack())
	f.request("movie")

	f.reconcile("movie")
	got := f.get("movie")
	assert.Empty(t, got.Status.Existing, "an extractable track satisfies nothing until it is written out")
	assert.Equal(t, []string{"de", "en"}, dispatched(f), "en is fetched: the embedded provider extracts it")
	assert.NotNil(t, item(t, got, "en").NextSearchAt)
	assert.Contains(t, cond(got, subtitlev1alpha1.SubtitleRequestConditionPlanned).Message, "spec.embedded.extract")

	// The worker writes it out; the sidecar then satisfies en.
	f.sidecar("movie.en.srt")
	f.reconcile("movie")
	got = f.get("movie")
	require.Len(t, got.Status.Existing, 1)
	assert.Equal(t, subtitlev1alpha1.SubtitleSourceSidecar, got.Status.Existing[0].Source)
	assert.Equal(t, "en", got.Status.Existing[0].LangKey)
}

func TestWithExtractOffAnEmbeddedTrackCountsAsExisting(t *testing.T) {
	f := newFixture(t, "sr-extract-off")
	f.profile(func(s *subtitlev1alpha1.SubtitleProfileSpec) { s.Embedded.Extract = ptr.To(false) })
	f.embeddedProvider("embedded")
	f.mediaFile("movie", englishTrack())
	f.request("movie")

	f.reconcile("movie")
	got := f.get("movie")
	require.Len(t, got.Status.Existing, 1)
	assert.Equal(t, subtitlev1alpha1.SubtitleSourceEmbedded, got.Status.Existing[0].Source)
	assert.Equal(t, []string{"de"}, dispatched(f))
}

// Extraction needs an extractor. With none the fetch worker could task --
// here the only embedded provider is disabled -- the track counts as
// existing, exactly as before; treating it as wanted would send en to the
// remote providers for a subtitle the file already carries. Enabling the
// provider makes it a want on the next plan (TestWatchesWakeTheController
// proves a provider change triggers that plan).
func TestExtractWithoutAnExtractorLeavesTheTrackExisting(t *testing.T) {
	f := newFixture(t, "sr-extract-none")
	f.profile(nil)
	sp := f.embeddedProvider("embedded")
	sp.Spec.Enabled = ptr.To(false)
	require.NoError(t, f.c.Update(f.ctx, sp))
	f.mediaFile("movie", englishTrack())
	f.request("movie")

	f.reconcile("movie")
	got := f.get("movie")
	require.Len(t, got.Status.Existing, 1, "no extractor: the embedded track still counts")
	assert.Equal(t, []string{"de"}, dispatched(f))

	sp.Spec.Enabled = ptr.To(true)
	require.NoError(t, f.c.Update(f.ctx, sp))
	f.reconcile("movie")
	assert.Empty(t, f.get("movie").Status.Existing)
	assert.Equal(t, []string{"de", "en"}, dispatched(f))
}
