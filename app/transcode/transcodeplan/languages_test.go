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

package transcodeplan

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/jobspec"
)

// The manager plans "original" from the item's original language, which
// the adapter reads (Inputs.OriginalLanguage): a Japanese film keeps its
// Japanese and English audio and drops the Spanish.
func TestThePlanReadsTheItemsOriginalLanguage(t *testing.T) {
	tp := newProfile("hevc", true, t0, nil)
	tp.Spec.Audio.Languages = []string{"en", "original"}
	st := probed("ph-1")
	st.MediaInfo.Audio = []commonv1.AudioStream{
		{Index: 1, Codec: "aac", Channels: 2, Language: "jpn", Default: true},
		{Index: 2, Codec: "aac", Channels: 2, Language: "eng"},
		{Index: 3, Codec: "aac", Channels: 2, Language: "spa"},
	}
	mf := heatFile(st)
	p, fail := PlanFor(mf.Spec.Path, &tp, jobspec.ProfileHash(tp.Spec), mf, st, nil, Device{}, "ja")
	require.Nil(t, fail)
	var langs []string
	for _, a := range p.Plan.Audio {
		langs = append(langs, a.Language)
	}
	assert.Equal(t, []string{"jpn", "eng"}, langs)
	assert.Contains(t, p.Plan.Dropped, "audio 2 (spa): not a kept language")
}

// The task carries the original language, so the worker plans as the
// manager did.
func TestTheTaskCarriesTheOriginalLanguage(t *testing.T) {
	in := queuedIn(t)
	in.OriginalLanguage = "ja"
	d := pass(in)
	require.NotNil(t, d.Publish, "a requested seq inside the window is republished")
	assert.Equal(t, "ja", d.Publish.OriginalLanguage)
}
